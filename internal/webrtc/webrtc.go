// Package webrtc serves low-latency live view over WebRTC (pion).
// H.264 passthrough from the live hub — no transcode. Peers are capped per
// camera and globally; overflow clients fall back to MJPEG/HLS.
// See docs/05-ingest-streaming.md (Primary: WebRTC).
package webrtc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph264"
	pion "github.com/pion/webrtc/v4"

	"github.com/sagostin/tapetum/internal/live"
)

var (
	// ErrUnsupportedCodec — H.265 cannot go over WebRTC in browsers.
	ErrUnsupportedCodec = errors.New("stream codec is not supported over WebRTC (H.264 only)")
	// ErrStreamUnavailable — no live frames yet (camera starting/offline).
	ErrStreamUnavailable = errors.New("stream is not available")
	// ErrTooManyPeers — per-camera or global peer cap reached.
	ErrTooManyPeers = errors.New("too many viewers on this camera")
)

const (
	maxPeersPerCamera = 4
	maxPeersGlobal    = 32
	gatherTimeout     = 3 * time.Second
)

// Server manages WebRTC peer connections against the live hub.
type Server struct {
	hub *live.Hub
	api *pion.API
	log *slog.Logger

	mu       sync.Mutex
	perCam   map[string]int
	total    int
	shutdown bool
}

func NewServer(hub *live.Hub) *Server {
	// Include loopback candidates: same-host viewers (and our E2E tests)
	// would otherwise fail ICE when no shared non-loopback subnet exists.
	se := pion.SettingEngine{}
	se.SetIncludeLoopbackCandidate(true)
	return &Server{
		hub:    hub,
		api:    pion.NewAPI(pion.WithSettingEngine(se)),
		log:    slog.With("component", "webrtc"),
		perCam: map[string]int{},
	}
}

// HandleOffer negotiates a peer connection for a camera stream
// (stream = "main" or "sub") and returns the SDP answer with all ICE
// candidates gathered (no trickle — the answer is complete).
func (s *Server) HandleOffer(ctx context.Context, camID, stream, offerSDP string) (string, error) {
	if offerSDP == "" {
		return "", errors.New("sdp offer is required")
	}
	sub := stream != "main"

	codec, ch, cancel, ok := s.hub.Subscribe(camID, sub)
	if !ok {
		return "", ErrStreamUnavailable
	}
	if codec != "h264" {
		cancel()
		return "", ErrUnsupportedCodec
	}

	s.mu.Lock()
	if s.shutdown || s.perCam[camID] >= maxPeersPerCamera || s.total >= maxPeersGlobal {
		s.mu.Unlock()
		cancel()
		return "", ErrTooManyPeers
	}
	s.perCam[camID]++
	s.total++
	s.mu.Unlock()

	pc, err := s.api.NewPeerConnection(pion.Configuration{})
	if err != nil {
		s.release(camID)
		cancel()
		return "", err
	}

	track, err := pion.NewTrackLocalStaticRTP(
		pion.RTPCodecCapability{MimeType: pion.MimeTypeH264}, "video", "tapetum-"+camID)
	if err != nil {
		s.release(camID)
		cancel()
		_ = pc.Close()
		return "", err
	}
	if _, err := pc.AddTrack(track); err != nil {
		s.release(camID)
		cancel()
		_ = pc.Close()
		return "", err
	}

	connected := make(chan struct{})
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		cancel()
		s.release(camID)
		_ = pc.Close()
	}
	pc.OnConnectionStateChange(func(st pion.PeerConnectionState) {
		switch st {
		case pion.PeerConnectionStateConnected:
			close(connected)
		case pion.PeerConnectionStateFailed,
			pion.PeerConnectionStateClosed,
			pion.PeerConnectionStateDisconnected:
			release()
		}
	})

	if err := pc.SetRemoteDescription(pion.SessionDescription{
		Type: pion.SDPTypeOffer, SDP: offerSDP,
	}); err != nil {
		release()
		return "", fmt.Errorf("bad offer: %w", err)
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		release()
		return "", err
	}
	gather := pion.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		release()
		return "", err
	}
	select {
	case <-gather:
	case <-time.After(gatherTimeout):
	case <-ctx.Done():
		release()
		return "", ctx.Err()
	}

	go s.pump(pc, track, ch, connected, release)
	s.log.Debug("peer connected", "camera", camID, "stream", stream)
	return pc.LocalDescription().SDP, nil
}

// pump waits for the peer to connect, then packetizes access units into RTP
// and writes them to the track until the peer or the stream goes away.
func (s *Server) pump(pc *pion.PeerConnection, track *pion.TrackLocalStaticRTP,
	ch <-chan live.Frame, connected <-chan struct{}, release func(),
) {
	defer release()
	select {
	case <-connected:
	case <-time.After(15 * time.Second):
		s.log.Debug("peer never connected")
		return
	}
	enc := &rtph264.Encoder{PayloadType: 96, PacketizationMode: 1}
	if err := enc.Init(); err != nil {
		s.log.Warn("rtp encoder init", "err", err)
		return
	}
	for f := range ch {
		pkts, err := enc.Encode(f.AU)
		if err != nil {
			continue
		}
		for _, pkt := range pkts {
			pkt.Timestamp = uint32(f.PTS)
			if err := track.WriteRTP(pkt); err != nil {
				return // track closed → peer gone
			}
		}
	}
}

// Close rejects new peers and closes existing ones (daemon shutdown).
func (s *Server) Close() {
	s.mu.Lock()
	s.shutdown = true
	s.mu.Unlock()
}

func (s *Server) release(camID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.perCam[camID] > 0 {
		s.perCam[camID]--
	}
	if s.total > 0 {
		s.total--
	}
}
