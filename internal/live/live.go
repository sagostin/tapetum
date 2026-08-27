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
	codec string
	sps   []byte
	pps   []byte
	vps   []byte
	ring  []Frame
	subs  map[chan Frame]struct{}
}

// Hub fans out frames per (camera, stream) pair.
type Hub struct {
	mu      sync.Mutex
	streams map[streamKey]*streamState
}

type streamKey struct {
	camID string
	sub   bool
}

func NewHub() *Hub {
	return &Hub{streams: map[streamKey]*streamState{}}
}

// Begin resets the stream state on a new ingest session (codec params may
// have changed); existing subscribers are kicked so they resubscribe with
// fresh codec parameters at the next keyframe.
func (h *Hub) Begin(camID string, sub bool, codec string, sps, pps, vps []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	k := streamKey{camID, sub}
	st, ok := h.streams[k]
	if !ok {
		st = &streamState{subs: map[chan Frame]struct{}{}}
		h.streams[k] = st
	}
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
	h.mu.Lock()
	defer h.mu.Unlock()
	st, ok := h.streams[streamKey{camID, sub}]
	if !ok {
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
// latest keyframe) is replayed into the channel before live frames.
// ok=false means the stream has no active session.
func (h *Hub) Subscribe(camID string, sub bool) (codec string, ch <-chan Frame, cancel func(), ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	st, exists := h.streams[streamKey{camID, sub}]
	if !exists || len(st.ring) == 0 {
		return "", nil, func() {}, false
	}
	c := make(chan Frame, subBuf)
	for _, f := range st.ring {
		c <- f
	}
	st.subs[c] = struct{}{}
	cancel = func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(st.subs, c)
	}
	return st.codec, c, cancel, true
}

// Codec returns the active codec ("h264"/"h265") for a stream, "" if none.
func (h *Hub) Codec(camID string, sub bool) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if st, ok := h.streams[streamKey{camID, sub}]; ok {
		return st.codec
	}
	return ""
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
