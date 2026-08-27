package record

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4/seekablebuffer"

	"github.com/sagostin/tapetum/internal/storage"
)

const (
	// videoClock is the RTP/H26x clock rate, used as the fMP4 video timescale.
	videoClock = 90000
	// targetSegDur / maxSegWait: cut at first IDR once the segment is at
	// least targetSegDur old; never cut mid-GOP (docs/05).
	targetSegDur = 6 * time.Second
)

// Recorder consumes access units of one camera's main stream and writes
// codec-copy fMP4 segments (init.mp4 separate, one moof+mdat per file).
// Not safe for concurrent use of Write* from multiple goroutines per track —
// ingest calls these from a single stream worker goroutine.
type Recorder struct {
	camID   string
	store   *Store
	backend storage.Backend
	log     *slog.Logger

	mu        sync.Mutex
	vcodec    string // "h264" or "h265"
	audioRate int64  // audio timescale; 0 = no audio track

	open      bool
	vSamples  []*fmp4.Sample
	vPTS      []int64
	aSamples  []*fmp4.Sample
	aPTS      []int64
	vFirstPTS int64
	aFirstPTS int64
	startNTP  time.Time
	lastNTP   time.Time
	seq       uint32
}

func NewRecorder(camID string, store *Store, backend storage.Backend) *Recorder {
	return &Recorder{
		camID:   camID,
		store:   store,
		backend: backend,
		log:     slog.With("component", "recorder", "camera", camID),
	}
}

// SetVideoCodec selects the video codec ("h264"/"h265"). Must be called
// before WriteVideo.
func (r *Recorder) SetVideoCodec(codec string) {
	r.mu.Lock()
	r.vcodec = codec
	r.mu.Unlock()
}

// SetAudio enables an audio track with the given timescale (e.g. 48000).
func (r *Recorder) SetAudio(timescale int64) {
	r.mu.Lock()
	r.audioRate = timescale
	r.mu.Unlock()
}

func (r *Recorder) isRandomAccess(au [][]byte) bool {
	if r.vcodec == "h265" {
		return h265.IsRandomAccess(au)
	}
	return h264.IsRandomAccess(au)
}

// WriteVideo appends a video access unit. pts is in 90 kHz units; ntp is the
// wall-clock time of the AU (from RTCP mapping or arrival time fallback).
func (r *Recorder) WriteVideo(au [][]byte, pts int64, ntp time.Time) error {
	randAcc := r.isRandomAccess(au)

	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.open {
		if !randAcc {
			return nil // wait for an IDR to open a segment
		}
		r.open = true
		r.vFirstPTS = pts
		r.startNTP = ntp
	} else if randAcc && ntp.Sub(r.startNTP) >= targetSegDur {
		// cut before this IDR: it starts the next segment
		if err := r.closeLocked(ntp); err != nil {
			r.log.Warn("segment close failed", "err", err)
		}
		r.open = true
		r.vFirstPTS = pts
		r.startNTP = ntp
	}

	avcc, err := h264.AVCC(au).Marshal()
	if err != nil {
		return err
	}
	r.vSamples = append(r.vSamples, &fmp4.Sample{
		IsNonSyncSample: !randAcc,
		Payload:         avcc,
	})
	r.vPTS = append(r.vPTS, pts)
	r.lastNTP = ntp
	return nil
}

// WriteAudio appends an AAC access unit (raw frame, no ADTS). pts is in
// audioRate units.
func (r *Recorder) WriteAudio(au []byte, pts int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.open || r.audioRate == 0 {
		return nil
	}
	if len(r.aSamples) == 0 {
		r.aFirstPTS = pts
	}
	cp := make([]byte, len(au))
	copy(cp, au)
	r.aSamples = append(r.aSamples, &fmp4.Sample{Payload: cp})
	r.aPTS = append(r.aPTS, pts)
	return nil
}

// Close flushes any open segment (disconnect/shutdown).
func (r *Recorder) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.open {
		if err := r.closeLocked(time.Now()); err != nil {
			r.log.Warn("segment flush failed", "err", err)
		}
	}
}

// closeLocked finalizes the open segment: assigns durations, marshals the
// fMP4 part, stores the file, and inserts the index row.
// nextSegStart is the wall time of the next segment's first frame (used only
// as a fallback end time).
func (r *Recorder) closeLocked(nextSegStart time.Time) error {
	r.open = false
	defer func() {
		r.vSamples, r.vPTS = nil, nil
		r.aSamples, r.aPTS = nil, nil
	}()

	if len(r.vSamples) == 0 {
		return nil
	}

	// Video durations from PTS deltas (decode order assumed ≈ arrival order;
	// B-frame reordering is not handled — most IP cameras don't send B-frames).
	assignDurations(r.vSamples, r.vPTS, r.vFirstPTS)

	// Audio durations likewise (typically constant 1024 at audioRate).
	if len(r.aSamples) > 0 {
		assignDurations(r.aSamples, r.aPTS, r.aFirstPTS)
	}

	start := r.startNTP
	end := r.lastNTP
	if len(r.vSamples) > 0 {
		last := r.vSamples[len(r.vSamples)-1]
		end = end.Add(time.Duration(last.Duration) * time.Second / videoClock)
	}
	if end.Before(start) || end.IsZero() {
		end = nextSegStart
	}

	tracks := []*fmp4.PartTrack{{
		ID:       1,
		BaseTime: baseTime(start, videoClock),
		Samples:  r.vSamples,
	}}
	if len(r.aSamples) > 0 && r.audioRate > 0 {
		tracks = append(tracks, &fmp4.PartTrack{
			ID:       2,
			BaseTime: baseTime(start, r.audioRate),
			Samples:  r.aSamples,
		})
	}

	r.seq++
	part := &fmp4.Part{SequenceNumber: r.seq, Tracks: tracks}
	buf := &seekablebuffer.Buffer{}
	if err := part.Marshal(buf); err != nil {
		return fmt.Errorf("marshal part: %w", err)
	}

	key := storage.SegmentKey(r.camID, start)
	size := int64(len(buf.Bytes()))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := r.backend.Put(ctx, key, bytes.NewReader(buf.Bytes()), size); err != nil {
		return fmt.Errorf("store %s: %w", key, err)
	}
	if err := r.store.InsertSegment(ctx, r.camID, start, end, key, size); err != nil {
		// index insert failed after file write → remove orphan
		_ = r.backend.Delete(ctx, key)
		return fmt.Errorf("index %s: %w", key, err)
	}
	return nil
}

// assignDurations sets Duration and PTSOffset on samples from their PTS
// series. dts_i = cumulative duration; ptsOffset_i = (pts_i - first) - dts_i.
func assignDurations(samples []*fmp4.Sample, pts []int64, firstPTS int64) {
	var prevDelta int64 = 1
	var dts int64
	for i, s := range samples {
		var delta int64
		if i+1 < len(pts) {
			delta = pts[i+1] - pts[i]
			if delta <= 0 {
				delta = prevDelta
			}
		} else {
			delta = prevDelta
		}
		prevDelta = delta
		s.Duration = uint32(delta)
		s.PTSOffset = int32((pts[i] - firstPTS) - dts)
		dts += delta
	}
}

// baseTime is the fMP4 tfdt base media decode time: wall-clock mapped onto
// the track timescale so ffmpeg concat across segments produces a correct
// absolute timeline (hls.js relies on playlist timing instead).
func baseTime(t time.Time, timescale int64) uint64 {
	return uint64(t.Unix())*uint64(timescale) +
		uint64(t.Nanosecond()/1e6)*uint64(timescale)/1000
}

// BuildInit marshals the fMP4 init segment for a camera's tracks and returns
// it base64-encoded for storage in cameras.status_detail.init.
func BuildInit(vcodec string, sps, pps, vps []byte, audioRate, audioChannels int) (string, error) {
	init := &fmp4.Init{}
	switch vcodec {
	case "h264":
		init.Tracks = append(init.Tracks, &fmp4.InitTrack{
			ID: 1, TimeScale: videoClock,
			Codec: &fmp4.CodecH264{SPS: sps, PPS: pps},
		})
	case "h265":
		init.Tracks = append(init.Tracks, &fmp4.InitTrack{
			ID: 1, TimeScale: videoClock,
			Codec: &fmp4.CodecH265{VPS: vps, SPS: sps, PPS: pps},
		})
	default:
		return "", fmt.Errorf("unsupported video codec %q", vcodec)
	}
	if audioRate > 0 {
		init.Tracks = append(init.Tracks, &fmp4.InitTrack{
			ID: 2, TimeScale: uint32(audioRate),
			Codec: &fmp4.CodecMPEG4Audio{Config: mpeg4audio.AudioSpecificConfig{
				Type:         mpeg4audio.ObjectTypeAACLC,
				SampleRate:   audioRate,
				ChannelCount: audioChannels,
			}},
		})
	}
	buf := &seekablebuffer.Buffer{}
	if err := init.Marshal(buf); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}
