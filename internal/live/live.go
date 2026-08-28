// Package live is the in-process fan-out of decoded access units from ingest
// to live consumers (WebRTC peers). Each camera stream keeps a small ring
// from the latest keyframe so new viewers start instantly.
// See docs/05-ingest-streaming.md (Live View).
package live

import (
	"sync"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"
)

// Frame is one decoded video access unit.
type Frame struct {
	AU       [][]byte // NAL units
	PTS      int64    // 90 kHz clock
	Keyframe bool
}

// maxRingFrames caps the ring (~4s at 30fps).
const maxRingFrames = 120

// subBuf is the per-subscriber channel capacity; slow subscribers are dropped.
// Sized so a transient network/CPU hiccup doesn't immediately evict the
// viewer — at 30fps, 256 frames is ~8.5s of buffer, well past any normal
// browser decode stall. A truly stuck consumer (e.g. tab in background
// holding back-pressure for minutes) still gets dropped so the broadcaster
// never blocks behind one slow viewer.
const subBuf = 256

// subscriber wraps a raw channel with a one-shot close. The slow-consumer path
// in Offer and the cancel func returned from Subscribe both want to close the
// channel — sync.Once makes sure only one of them actually does, so the rebase
// goroutine that reads from raw always sees exactly one close signal.
type subscriber struct {
	raw       chan Frame
	closeOnce sync.Once
}

// close shuts down the raw channel. Safe to call multiple times.
func (s *subscriber) close() {
	s.closeOnce.Do(func() { close(s.raw) })
}

type streamState struct {
	mu     sync.Mutex
	codec  string
	sps    []byte
	pps    []byte
	vps    []byte
	ring   []Frame
	subs   map[*subscriber]struct{}
	closed bool // stream torn down; Offer returns early
}

// Hub fans out frames per (camera, stream) pair. Streams are sharded — each
// streamState owns its own mutex so Offer/Subscribe on one camera never block
// another. The streams map itself uses a sync.RWMutex; Offer/Subscribe use the
// streamState lock, not the map lock, on the hot path.
type Hub struct {
	mu      sync.RWMutex
	streams map[streamKey]*streamState
}

type streamKey struct {
	camID string
	sub   bool
}

func NewHub() *Hub {
	return &Hub{streams: map[streamKey]*streamState{}}
}

func (h *Hub) getOrCreate(k streamKey) *streamState {
	h.mu.RLock()
	st, ok := h.streams[k]
	h.mu.RUnlock()
	if ok {
		return st
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	st, ok = h.streams[k]
	if !ok {
		st = &streamState{subs: map[*subscriber]struct{}{}}
		h.streams[k] = st
	}
	return st
}

// Begin resets the stream state on a new ingest session (codec params may
// have changed); existing subscribers are kicked so they resubscribe with
// fresh codec parameters at the next keyframe.
func (h *Hub) Begin(camID string, sub bool, codec string, sps, pps, vps []byte) {
	st := h.getOrCreate(streamKey{camID, sub})
	st.mu.Lock()
	defer st.mu.Unlock()
	st.codec = codec
	st.sps, st.pps, st.vps = sps, pps, vps
	st.ring = nil
	for s := range st.subs {
		delete(st.subs, s)
		s.close()
	}
}

// Offer pushes one access unit to the ring and all subscribers.
func (h *Hub) Offer(camID string, sub bool, au [][]byte, pts int64) {
	h.mu.RLock()
	st, exists := h.streams[streamKey{camID, sub}]
	h.mu.RUnlock()
	if !exists {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.closed {
		return
	}
	f := Frame{AU: au, PTS: pts, Keyframe: isRandomAccess(st.codec, au)}
	if f.Keyframe {
		st.ring = nil
	}
	st.ring = append(st.ring, f)
	if len(st.ring) > maxRingFrames {
		st.ring = st.ring[len(st.ring)-maxRingFrames:]
	}
	for s := range st.subs {
		select {
		case s.raw <- f:
		default:
			// slow consumer: a dropped P-frame breaks decode, so drop the
			// subscriber instead — it will resubscribe at a keyframe
			delete(st.subs, s)
			s.close()
		}
	}
}

// Subscribe attaches a consumer to a stream. The current ring (from the
// latest keyframe) is replayed into the channel before live frames; the
// codec parameter sets are returned for decoder/bootstrap use.
//
// Every frame delivered to this subscriber has its PTS rebased so the first
// replayed ring frame is at PTS=0. Without this, a viewer joining a camera
// that's been running for hours would see a stream whose first sample sits
// 50+ minutes into the future — the browser's MSE timeline starts at 0 and
// the player shows a black tile until playback reaches that timestamp.
//
// The rebase is done by a per-subscriber goroutine that owns the raw channel
// registered in st.subs; Offer pushes absolute-PTS frames to it, the
// goroutine subtracts startPTS and forwards to the returned (rebased) channel.
// ok=false means the stream has no active session.
func (h *Hub) Subscribe(camID string, sub bool) (codec string, sps, pps, vps []byte, ch <-chan Frame, cancel func(), ok bool) {
	h.mu.RLock()
	st, exists := h.streams[streamKey{camID, sub}]
	h.mu.RUnlock()
	if !exists {
		return "", nil, nil, nil, nil, func() {}, false
	}
	st.mu.Lock()
	if len(st.ring) == 0 || st.closed {
		st.mu.Unlock()
		return "", nil, nil, nil, nil, func() {}, false
	}

	// Lock in the rebase origin before releasing st.mu so subsequent Offers
	// and ring appends can't move it under us. The first ring frame is always
	// a keyframe (Offer clears the ring on every keyframe), so the PTS is
	// stable and a safe rebase anchor.
	startPTS := st.ring[0].PTS

	newSub := &subscriber{raw: make(chan Frame, subBuf)}
	out := make(chan Frame, subBuf)

	// Replay the ring with rebased PTS — the consumer sees a stream that
	// starts at the latest keyframe, time 0, ready to play immediately.
	for _, f := range st.ring {
		out <- Frame{AU: f.AU, PTS: f.PTS - startPTS, Keyframe: f.Keyframe}
	}
	st.subs[newSub] = struct{}{}
	codec = st.codec
	sps, pps, vps = st.sps, st.pps, st.vps
	st.mu.Unlock()

	// Forward live frames (still absolute PTS in `f`) into the rebased
	// output channel. When raw closes — either via the slow-consumer path
	// in Offer or the cancel func below — we drain and close `out`.
	go func() {
		defer close(out)
		for f := range newSub.raw {
			out <- Frame{AU: f.AU, PTS: f.PTS - startPTS, Keyframe: f.Keyframe}
		}
	}()

	cancel = func() {
		st.mu.Lock()
		delete(st.subs, newSub)
		st.mu.Unlock()
		newSub.close()
	}
	return codec, sps, pps, vps, out, cancel, true
}

// SetParams updates the codec parameter sets of an active stream. Used when
// the parameters are learned in-band after Begin — e.g. H.265 cameras that
// omit VPS/SPS/PPS from the SDP but repeat them before every IDR.
func (h *Hub) SetParams(camID string, sub bool, sps, pps, vps []byte) {
	h.mu.RLock()
	st, ok := h.streams[streamKey{camID, sub}]
	h.mu.RUnlock()
	if !ok {
		return
	}
	st.mu.Lock()
	st.sps, st.pps, st.vps = sps, pps, vps
	st.mu.Unlock()
}

// Codec returns the active codec ("h264"/"h265") for a stream, "" if none.
func (h *Hub) Codec(camID string, sub bool) string {
	h.mu.RLock()
	st, ok := h.streams[streamKey{camID, sub}]
	h.mu.RUnlock()
	if !ok {
		return ""
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.codec
}

// remove drops a stream entirely. Called when ingest tears the stream down.
func (h *Hub) remove(camID string, sub bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	k := streamKey{camID, sub}
	st, ok := h.streams[k]
	if !ok {
		return
	}
	delete(h.streams, k)
	st.mu.Lock()
	st.closed = true
	for s := range st.subs {
		delete(st.subs, s)
		s.close()
	}
	st.mu.Unlock()
}

func isRandomAccess(codec string, au [][]byte) bool {
	switch codec {
	case "h264":
		return h264.IsRandomAccess(au)
	case "h265":
		return h265.IsRandomAccess(au)
	}
	return false
}
