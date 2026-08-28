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
const subBuf = 128

type streamState struct {
	mu     sync.Mutex
	codec  string
	sps    []byte
	pps    []byte
	vps    []byte
	ring   []Frame
	subs   map[chan Frame]struct{}
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
		st = &streamState{subs: map[chan Frame]struct{}{}}
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
	for ch := range st.subs {
		delete(st.subs, ch)
		close(ch)
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
	for ch := range st.subs {
		select {
		case ch <- f:
		default:
			// slow consumer: a dropped P-frame breaks decode, so drop the
			// subscriber instead — it will resubscribe at a keyframe
			delete(st.subs, ch)
			close(ch)
		}
	}
}

// Subscribe attaches a consumer to a stream. The current ring (from the
// latest keyframe) is replayed into the channel before live frames; the
// codec parameter sets are returned for decoder/bootstrap use.
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
	c := make(chan Frame, subBuf)
	for _, f := range st.ring {
		c <- f
	}
	st.subs[c] = struct{}{}
	codec = st.codec
	sps, pps, vps = st.sps, st.pps, st.vps
	st.mu.Unlock()

	cancel = func() {
		st.mu.Lock()
		delete(st.subs, c)
		st.mu.Unlock()
	}
	return codec, sps, pps, vps, c, cancel, true
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
	for ch := range st.subs {
		delete(st.subs, ch)
		close(ch)
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
