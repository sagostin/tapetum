package live

import (
	"fmt"
	"io"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4/seekablebuffer"
)

// videoClock is the H.264/H.265 clock rate used as the fMP4 track timescale
// (matches RTP and what cameras emit on the wire).
const videoClock = 90000

// Muxer writes a continuous fragmented MP4 stream: a single init segment
// (ftyp + moov) followed by one moof+mdat per access unit. No segment
// boundaries mid-stream — the player appendBuffer's chunks as they arrive.
//
// Used by the live fMP4 streaming endpoint (UniFi Protect-style over plain
// HTTP chunked transfer; ~100 ms fragments, browser does native MSE).
type Muxer struct {
	codec string // "h264" | "h265"
	sps   []byte
	pps   []byte
	vps   []byte
	seq   uint32

	written bool
	lastPTS int64
}

// NewMuxer returns a Muxer ready to write an init segment.
func NewMuxer(codec string, sps, pps, vps []byte) *Muxer {
	return &Muxer{codec: codec, sps: sps, pps: pps, vps: vps}
}

// Written reports whether WriteInit has been called.
func (m *Muxer) Written() bool { return m.written }

// WriteInit emits ftyp + moov with the codec parameter sets.
func (m *Muxer) WriteInit(w io.Writer) error {
	if m.written {
		return fmt.Errorf("live: init already written")
	}
	init := &fmp4.Init{}
	switch m.codec {
	case "h264":
		init.Tracks = append(init.Tracks, &fmp4.InitTrack{
			ID:        1,
			TimeScale: videoClock,
			Codec:     &fmp4.CodecH264{SPS: m.sps, PPS: m.pps},
		})
	case "h265":
		init.Tracks = append(init.Tracks, &fmp4.InitTrack{
			ID:        1,
			TimeScale: videoClock,
			Codec:     &fmp4.CodecH265{VPS: m.vps, SPS: m.sps, PPS: m.pps},
		})
	default:
		return fmt.Errorf("live: unsupported codec %q for fMP4 init", m.codec)
	}
	buf := &seekablebuffer.Buffer{}
	if err := init.Marshal(buf); err != nil {
		return err
	}
	_, err := w.Write(buf.Bytes())
	if err == nil {
		m.written = true
	}
	return err
}

// WriteSample emits a moof+mdat pair for one access unit. pts is in 90 kHz
// units (matching Frame.PTS). isIDR marks the sample as a sync point.
func (m *Muxer) WriteSample(w io.Writer, au [][]byte, pts int64, isIDR bool) error {
	if !m.written {
		return fmt.Errorf("live: init must be written before samples")
	}
	payload, err := h264.AVCC(au).Marshal()
	if err != nil {
		return err
	}
	duration := uint32(estimateFrameDuration(pts, m.lastPTS))
	if duration == 0 {
		duration = 3000 // 30 fps fallback for the very first sample
	}
	m.lastPTS = pts

	sample := &fmp4.Sample{
		Duration:        duration,
		PTSOffset:       0,
		IsNonSyncSample: !isIDR,
		Payload:         payload,
	}
	part := &fmp4.Part{
		SequenceNumber: m.seq,
		Tracks: []*fmp4.PartTrack{{
			ID:       1,
			BaseTime: uint64(pts),
			Samples:  []*fmp4.Sample{sample},
		}},
	}
	m.seq++

	buf := &seekablebuffer.Buffer{}
	if err := part.Marshal(buf); err != nil {
		return err
	}
	_, err = w.Write(buf.Bytes())
	return err
}

// estimateFrameDuration returns the delta to the previous PTS, clamped to a
// sane range. Returns 0 when there's no previous sample.
func estimateFrameDuration(cur, prev int64) uint32 {
	if prev <= 0 {
		return 0
	}
	d := cur - prev
	if d < 1 {
		return 1
	}
	// clamp to 1 second; bigger means there was a stream gap.
	if d > videoClock {
		return videoClock
	}
	return uint32(d)
}
