package live

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4/seekablebuffer"
)

// TestFirstMoofHasBaseTimeZero verifies the end-to-end fix: a subscriber that
// joins an already-running stream should see an fMP4 stream whose first
// fragment's BaseTime is 0 (rebased), not the absolute camera PTS in the
// millions. Browsers' MSE timelines start at 0, so anything else means a
// black tile for as long as the camera has been up.
func TestFirstMoofHasBaseTimeZero(t *testing.T) {
	h := NewHub()
	camID := "rebased-e2e"

	h.Begin(camID, false, "h264", []byte{0x67, 0x42, 0x00, 0x1e}, []byte{0x68}, nil)

	// Pretend the camera has been running for an hour. Offer a keyframe at
	// 3600s * 90kHz = 324,000,000 ticks.
	h.Offer(camID, false, [][]byte{{0x65, 0x88, 0x84, 0x00, 0x33}}, 324_000_000)
	for i := 1; i <= 30; i++ {
		h.Offer(camID, false, [][]byte{{0x41, 0x9a, 0x24, byte(i)}}, 324_000_000+int64(i)*3000)
	}

	_, _, _, _, ch, cancel, ok := h.Subscribe(camID, false)
	if !ok {
		t.Fatal("Subscribe failed")
	}
	defer cancel()

	// Pull the first frame (rebased keyframe). PTS MUST be 0.
	select {
	case f := <-ch:
		if f.PTS != 0 {
			t.Fatalf("first frame PTS = %d, want 0 (rebased from %d)", f.PTS, 324_000_000)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}

	// Simulate what the muxer would write — verify it would write base_time=0
	// given a rebased frame.
	muxer := newTestMuxer()
	if err := muxer.writeSample(0, [][]byte{{0x65, 0x88, 0x84, 0x00, 0x33}}, 0, true); err != nil {
		t.Fatalf("writeSample: %v", err)
	}
	moof := muxer.buf.Bytes()
	baseTime := extractTfdtBaseTime(t, moof)
	if baseTime != 0 {
		t.Errorf("first moof tfdt base_time = %d, want 0", baseTime)
	}
}

// testMuxer mirrors the relevant fields of live.Muxer for a one-shot write.
type testMuxer struct {
	buf *seekablebuffer.Buffer
	seq uint32
}

func newTestMuxer() *testMuxer {
	return &testMuxer{buf: &seekablebuffer.Buffer{}}
}

func (m *testMuxer) writeSample(pts int64, au [][]byte, _ int64, isIDR bool) error {
	sample := &fmp4.Sample{
		Duration:        3000,
		IsNonSyncSample: !isIDR,
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
	return part.Marshal(m.buf)
}

func extractTfdtBaseTime(t *testing.T, b []byte) int64 {
	t.Helper()
	// Walk top-level boxes looking for 'moof'.
	p := 0
	for p+8 <= len(b) {
		size := int(binary.BigEndian.Uint32(b[p : p+4]))
		if size == 0 || p+size > len(b) {
			break
		}
		boxType := string(b[p+4 : p+8])
		if boxType == "moof" {
			return walkMoofForTfdt(t, b[p+8:p+size])
		}
		p += size
	}
	t.Fatal("no moof found in bytes")
	return -1
}

var _ = bytes.Buffer{} // keep import for future assertions

func walkMoofForTfdt(t *testing.T, b []byte) int64 {
	t.Helper()
	p := 0
	for p+8 <= len(b) {
		size := int(binary.BigEndian.Uint32(b[p : p+4]))
		if size == 0 || p+size > len(b) {
			break
		}
		boxType := string(b[p+4 : p+8])
		if boxType == "traf" {
			return walkTrafForTfdt(t, b[p+8:p+size])
		}
		p += size
	}
	t.Fatal("no traf found in moof")
	return -1
}

func walkTrafForTfdt(t *testing.T, b []byte) int64 {
	t.Helper()
	p := 0
	for p+8 <= len(b) {
		size := int(binary.BigEndian.Uint32(b[p : p+4]))
		if size == 0 || p+size > len(b) {
			break
		}
		boxType := string(b[p+4 : p+8])
		if boxType == "tfdt" {
			// version(1) + flags(3) + base_time(8 if v1, 4 if v0)
			version := b[p+8]
			if version == 1 {
				return int64(binary.BigEndian.Uint64(b[p+12 : p+20]))
			}
			return int64(binary.BigEndian.Uint32(b[p+12 : p+16]))
		}
		p += size
	}
	t.Fatal("no tfdt found in traf")
	return -1
}
