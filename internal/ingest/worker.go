package ingest

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph264"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph265"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtpmpeg4audio"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"
	"github.com/pion/rtp"

	"github.com/sagostin/tapetum/internal/camera"
	"github.com/sagostin/tapetum/internal/live"
	"github.com/sagostin/tapetum/internal/record"
	"github.com/sagostin/tapetum/internal/storage"
	"github.com/sagostin/tapetum/internal/ws"
)

// backoff steps from docs/05: 1s → 2s → 5s → 15s → 60s cap, jittered.
var backoff = []time.Duration{
	1 * time.Second, 2 * time.Second, 5 * time.Second, 15 * time.Second, 60 * time.Second,
}

// cameraWorker supervises the main (+ optional sub) stream sessions of one
// camera and owns its live status transitions.
type cameraWorker struct {
	cam     *camera.Camera // snapshot; Sync restarts the worker on changes
	cams    *camera.Store
	segs    *record.Store
	backend storage.Backend
	hub     *ws.Hub
	live    *live.Hub
	snap    *snapshotSet
	log     *slog.Logger

	// decodeLogSampler throttles per-packet decode error logs to one per
	// (error message, second) per stream — packet reordering and small
	// losses are normal under UDP/TCP jitter and we don't want them
	// drowning the log + burning CPU on allocations.
	decodeLogSampler map[string]time.Time

	ctx    context.Context
	cancel context.CancelFunc

	recorder *record.Recorder

	mu          sync.Mutex
	mainUp      bool
	subUp       bool
	lastFrameAt time.Time
	bytesTotal  atomic.Int64
	framesTotal atomic.Int64
	mainBytes   atomic.Int64 // main stream only — status bitrate/fps
	mainFrames  atomic.Int64
	startedAt   time.Time
	status      camera.Status
	detail      map[string]any
}

func newCameraWorker(cam *camera.Camera, cams *camera.Store, segs *record.Store,
	backend storage.Backend, hub *ws.Hub, liveHub *live.Hub, snap *snapshotSet, log *slog.Logger,
) *cameraWorker {
	ctx, cancel := context.WithCancel(context.Background())
	rec := record.NewRecorder(cam.ID, segs, backend)
	rec.SetMode(cam.RecordMode)
	return &cameraWorker{
		cam:              cam,
		cams:             cams,
		segs:             segs,
		backend:          backend,
		hub:              hub,
		live:             liveHub,
		snap:             snap,
		log:              log.With("camera", cam.Name, "camera_id", cam.ID),
		ctx:              ctx,
		cancel:           cancel,
		recorder:         rec,
		detail:           map[string]any{},
		status:           camera.StatusOffline, // zero value "" would violate the DB check
		decodeLogSampler: map[string]time.Time{},
	}
}

// logDecodeErr logs a per-packet decode error at most once per message per
// second, so a single bad source can't drown the log under thousands of
// identical "invalid FU-A packet" lines and burn CPU on allocations.
func (w *cameraWorker) logDecodeErr(msg string) {
	now := time.Now()
	if last, ok := w.decodeLogSampler[msg]; ok && now.Sub(last) < time.Second {
		return
	}
	w.decodeLogSampler[msg] = now
	w.log.Debug(msg)
}

func (w *cameraWorker) stop() { w.cancel() }

// run starts the stream session loops and the health ticker.
func (w *cameraWorker) run() {
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		w.sessionLoop(w.cam.MainURL, false)
	}()
	if w.cam.SubURL != nil && *w.cam.SubURL != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.sessionLoop(*w.cam.SubURL, true)
		}()
	}

	// health ticker: bitrate/fps computation + status transitions
	// (main-stream counters only — the sub stream would double the numbers)
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	var lastBytes, lastFrames int64
	for {
		select {
		case <-w.ctx.Done():
			wg.Wait()
			w.recorder.Close()
			w.setStatus(camera.StatusOffline, map[string]any{"last_error": "worker stopped"})
			return
		case <-t.C:
			b := w.mainBytes.Load()
			f := w.mainFrames.Load()
			bitrateKbps := float64(b-lastBytes) * 8 / 5 / 1000
			fps := float64(f-lastFrames) / 5
			lastBytes, lastFrames = b, f

			w.mu.Lock()
			up := w.mainUp
			lastFrame := w.lastFrameAt
			w.mu.Unlock()

			var st camera.Status
			switch {
			case !up:
				st = camera.StatusOffline
			case time.Since(lastFrame) > 5*time.Second:
				st = camera.StatusDegraded
			default:
				st = camera.StatusOnline
			}
			w.setStatus(st, map[string]any{
				"bitrate_kbps": int(bitrateKbps),
				"fps":          fps,
				"uptime":       formatDuration(time.Since(w.startedAt)),
			})
		}
	}
}

// sessionLoop maintains one RTSP session with reconnect backoff.
func (w *cameraWorker) sessionLoop(rawURL string, isSub bool) {
	attempt := 0
	for {
		err := w.runSession(rawURL, isSub)
		if errors.Is(err, context.Canceled) || w.ctx.Err() != nil {
			return
		}
		w.log.Warn("stream session ended", "sub", isSub, "err", err)
		w.markUp(isSub, false)
		if !isSub {
			w.recorder.Close()
			w.setDetail("last_error", err.Error())
			_ = w.segs.OpenGap(context.Background(), w.cam.ID, "ingest: "+err.Error())
		}

		d := backoff[min(attempt, len(backoff)-1)]
		d = d + time.Duration(rand.Int63n(int64(d)/5)) // ±20% jitter
		attempt++
		select {
		case <-w.ctx.Done():
			return
		case <-time.After(d):
		}
	}
}

// runSession runs a single RTSP connection until error.
func (w *cameraWorker) runSession(rawURL string, isSub bool) error {
	u, err := w.buildURL(rawURL)
	if err != nil {
		return err
	}

	c := &gortsplib.Client{
		Scheme:       u.Scheme,
		Host:         u.Host,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 10 * time.Second,
		Protocol:     protocolOf(w.cam.Transport),
		// Userspace-side UDP socket buffer; with the compose sysctl
		// (net.core.rmem_max=8MB) this absorbs 8MP bitrate bursts instead of
		// dropping RTP at the kernel queue. Only used for transport=udp.
		UDPReadBufferSize: 4 << 20,
	}
	if err := c.Start(); err != nil {
		return err
	}
	defer c.Close()

	desc, _, err := c.Describe(u)
	if err != nil {
		return err
	}

	// video: H264 or H265
	var h264f *format.H264
	h264Media := desc.FindFormat(&h264f)
	var h265f *format.H265
	var h265Media *description.Media
	if h264Media == nil {
		h265Media = desc.FindFormat(&h265f)
	}
	if h264Media == nil && h265Media == nil {
		return errors.New("no H264/H265 video in stream")
	}

	// audio: AAC only (PCMA needs transcode — skipped in phase 1)
	var aac *format.MPEG4Audio
	aacMedia := desc.FindFormat(&aac)

	medias := []*description.Media{}
	if h264Media != nil {
		medias = append(medias, h264Media)
	} else {
		medias = append(medias, h265Media)
	}
	if !isSub && aacMedia != nil {
		medias = append(medias, aacMedia)
	}

	var rtpDec264 *rtph264.Decoder
	var rtpDec265 *rtph265.Decoder
	var rtpDecAAC *rtpmpeg4audio.Decoder
	vcodec := ""
	var sps, pps, vps []byte

	if h264Media != nil {
		rtpDec264, err = h264f.CreateDecoder()
		vcodec = "h264"
		sps, pps = h264f.SPS, h264f.PPS
	} else {
		rtpDec265, err = h265f.CreateDecoder()
		vcodec = "h265"
		sps, pps, vps = h265f.SPS, h265f.PPS, h265f.VPS
	}
	if err != nil {
		return err
	}
	if aacMedia != nil && !isSub {
		rtpDecAAC, err = aac.CreateDecoder()
		if err != nil {
			rtpDecAAC = nil // video-only fallback
		}
	}

	// Some H.265 cameras omit VPS/SPS/PPS from the SDP but repeat them
	// in-band before every IDR — recover them from the first keyframe so the
	// recorder init segment, live subscribers, and snapshots all get them.
	h265NeedParams := vcodec == "h265" && (len(vps) == 0 || len(sps) == 0 || len(pps) == 0)

	// buildInit marshals the recorder's fMP4 init segment into status_detail.
	// Deferred for H.265 cameras whose params arrive in-band (see above).
	buildInit := func() {
		initB64, err := record.BuildInit(vcodec, sps, pps, vps,
			audioRateOf(aac), audioChannelsOf(aac))
		if err == nil {
			w.setDetail("init", initB64)
		} else {
			w.log.Warn("build init segment", "err", err)
		}
	}

	if err := c.SetupAll(desc.BaseURL, medias); err != nil {
		return err
	}

	// wire packet callbacks
	if h264Media != nil {
		c.OnPacketRTP(h264Media, h264f, func(pkt *rtp.Packet) {
			pts, ptsOK := c.PacketPTS(h264Media, pkt)
			ntp, ntpOK := c.PacketNTP(h264Media, pkt)
			au, err := rtpDec264.Decode(pkt)
			if err != nil {
				if !errors.Is(err, rtph264.ErrNonStartingPacketAndNoPrevious) &&
					!errors.Is(err, rtph264.ErrMorePacketsNeeded) {
					w.logDecodeErr("h264 decode err=" + err.Error())
				}
				return
			}
			w.onVideoAU(au, pts, ptsOK, ntp, ntpOK, isSub, "h264", sps, pps, nil)
		})
	} else {
		c.OnPacketRTP(h265Media, h265f, func(pkt *rtp.Packet) {
			pts, ptsOK := c.PacketPTS(h265Media, pkt)
			ntp, ntpOK := c.PacketNTP(h265Media, pkt)
			au, err := rtpDec265.Decode(pkt)
			if err != nil {
				if !errors.Is(err, rtph265.ErrNonStartingPacketAndNoPrevious) &&
					!errors.Is(err, rtph265.ErrMorePacketsNeeded) {
					w.logDecodeErr("h265 decode err=" + err.Error())
				}
				return
			}
			if h265NeedParams && h265.IsRandomAccess(au) {
				if v, s, p := extractH265Params(au); len(v) > 0 && len(s) > 0 && len(p) > 0 {
					vps, sps, pps = v, s, p
					h265NeedParams = false
					w.live.SetParams(w.cam.ID, isSub, sps, pps, vps)
					w.log.Info("h265 parameters recovered in-band", "sub", isSub)
					if !isSub {
						buildInit()
					}
				}
			}
			w.onVideoAU(au, pts, ptsOK, ntp, ntpOK, isSub, "h265", sps, pps, vps)
		})
	}
	if rtpDecAAC != nil {
		c.OnPacketRTP(aacMedia, aac, func(pkt *rtp.Packet) {
			pts, ptsOK := c.PacketPTS(aacMedia, pkt)
			aus, err := rtpDecAAC.Decode(pkt)
			if err != nil || !ptsOK {
				return
			}
			for _, au := range aus {
				_ = w.recorder.WriteAudio(au, pts)
			}
		})
	}

	// recorder + init segment setup (main stream only)
	if !isSub {
		w.recorder.SetVideoCodec(vcodec)
		if rtpDecAAC != nil && aac.Config != nil {
			w.recorder.SetAudio(int64(aac.Config.SampleRate))
		}
		w.setDetail("codec", vcodec)
		if !h265NeedParams {
			buildInit()
		}
	}

	if _, err := c.Play(nil); err != nil {
		return err
	}

	// reset live fan-out state for the new session (codec params may differ)
	w.live.Begin(w.cam.ID, isSub, vcodec, sps, pps, vps)

	w.startedAt = time.Now()
	w.markUp(isSub, true)
	if !isSub {
		_ = w.segs.CloseGaps(context.Background(), w.cam.ID)
	}
	w.log.Info("stream connected", "sub", isSub, "codec", vcodec)

	return c.Wait()
}

// onVideoAU feeds the recorder (main) and the snapshot cache (both streams).
func (w *cameraWorker) onVideoAU(au [][]byte, pts int64, ptsOK bool, ntp time.Time, ntpOK bool,
	isSub bool, vcodec string, sps, pps, vps []byte,
) {
	n := int64(auLen(au))
	w.bytesTotal.Add(n)
	w.framesTotal.Add(1)
	if !isSub {
		w.mainBytes.Add(n)
		w.mainFrames.Add(1)
	}
	w.mu.Lock()
	w.lastFrameAt = time.Now()
	w.mu.Unlock()

	if !ntpOK {
		ntp = time.Now() // camera without RTCP SR: arrival-time fallback
	}
	if !isSub && ptsOK {
		if err := w.recorder.WriteVideo(au, pts, ntp); err != nil {
			w.log.Warn("record write", "err", err)
		}
	}
	if ptsOK {
		w.live.Offer(w.cam.ID, isSub, au, pts)
	}
	w.snap.offer(isSub, au, vcodec, sps, pps, vps)
}

func (w *cameraWorker) markUp(isSub, up bool) {
	w.mu.Lock()
	if isSub {
		w.subUp = up
	} else {
		w.mainUp = up
	}
	w.mu.Unlock()
}

// setDetail merges a key into status_detail and persists immediately.
func (w *cameraWorker) setDetail(key string, v any) {
	w.mu.Lock()
	w.detail[key] = v
	st := w.status
	detail := copyDetail(w.detail)
	w.mu.Unlock()
	w.writeStatus(st, detail)
}

// setStatus transitions the camera status (broadcast on change, periodic
// detail refresh otherwise).
func (w *cameraWorker) setStatus(st camera.Status, extra map[string]any) {
	w.mu.Lock()
	changed := w.status != st
	w.status = st
	for k, v := range extra {
		w.detail[k] = v
	}
	detail := copyDetail(w.detail)
	w.mu.Unlock()
	w.writeStatus(st, detail)
	if changed {
		w.hub.Broadcast("camera.status", map[string]any{
			"camera_id": w.cam.ID,
			"status":    st,
			"detail":    detail,
		})
	}
}

func (w *cameraWorker) writeStatus(st camera.Status, detail map[string]any) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.cams.SetStatus(ctx, w.cam.ID, st, detail); err != nil {
		w.log.Debug("status write", "err", err)
	}
}

func (w *cameraWorker) stats() map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := copyDetail(w.detail)
	out["running"] = true
	out["status"] = w.status
	out["main_up"] = w.mainUp
	out["sub_up"] = w.subUp
	out["bytes_total"] = w.bytesTotal.Load()
	out["frames_total"] = w.framesTotal.Load()
	if !w.lastFrameAt.IsZero() {
		out["last_frame_age_s"] = time.Since(w.lastFrameAt).Seconds()
	}
	if !w.startedAt.IsZero() {
		out["uptime"] = formatDuration(time.Since(w.startedAt))
	}
	return out
}

func copyDetail(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// buildURL injects camera credentials into the RTSP URL when absent.
func (w *cameraWorker) buildURL(raw string) (*base.URL, error) {
	if !strings.Contains(raw, "://") {
		raw = "rtsp://" + raw
	}
	pu, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if pu.User == nil && w.cam.Username != "" {
		pass, err := w.cams.DecryptPassword(w.cam.PasswordEnc)
		if err != nil {
			return nil, err
		}
		pu.User = url.UserPassword(w.cam.Username, pass)
	}
	return base.ParseURL(pu.String())
}

func protocolOf(transport string) *gortsplib.Protocol {
	var p gortsplib.Protocol
	switch transport {
	case "udp":
		p = gortsplib.ProtocolUDP
	case "auto":
		// "auto" = TCP: reliable, lossless delivery is what an NVR wants;
		// UDP's lower latency doesn't justify packet loss (corrupted frames,
		// NACK storms on WebRTC fan-out) on LAN camera links.
		p = gortsplib.ProtocolTCP
	default:
		p = gortsplib.ProtocolTCP
	}
	return &p
}

func audioRateOf(aac *format.MPEG4Audio) int {
	if aac == nil || aac.Config == nil {
		return 0
	}
	return aac.Config.SampleRate
}

func audioChannelsOf(aac *format.MPEG4Audio) int {
	if aac == nil || aac.Config == nil {
		return 0
	}
	return aac.Config.ChannelCount
}

func auLen(au [][]byte) int {
	n := 0
	for _, nalu := range au {
		n += len(nalu)
	}
	return n
}

// extractH265Params pulls VPS/SPS/PPS NAL units out of an access unit —
// cameras that omit them from the SDP typically repeat them in-band before
// each IDR.
func extractH265Params(au [][]byte) (vps, sps, pps []byte) {
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}
		switch (nalu[0] >> 1) & 0x3F {
		case 32:
			vps = nalu
		case 33:
			sps = nalu
		case 34:
			pps = nalu
		}
	}
	return
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
