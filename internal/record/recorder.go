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
	// least targetSegDur old; never cut mid-GOP (docs/05). Kept short
	// (1s) so the live HLS playlist serves each tile with ~1-2s of glass-
	// to-glass latency (UI3-style). Trades more recording_segments rows
	// (1 per second per camera) for live latency — Postgres handles this
	// trivially.
	targetSegDur = 1 * time.Second
	// preRollRing is how much footage record_mode=motion keeps before an
	// event starts (docs/05: 10s ring buffer).
	preRollRing = 10 * time.Second
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

	// record_mode state ("off" drops everything; "motion" keeps a pre-roll
	// ring until MotionActive flips on)
	mode         string // "continuous" (default), "motion", "off"
	motionActive bool
	vNTP         []time.Time // parallel to vSamples; motion-mode ring only
}

func NewRecorder(camID string, store *Store, backend storage.Backend) *Recorder {
	return &Recorder{
		camID:   camID,
		store:   store,
		backend: backend,
		log:     slog.With("component", "recorder", "camera", camID),
	}
}

// SetMode selects the record mode: "continuous" (default), "motion"
// (pre-roll ring, persists only during motion windows), "off" (no recording).
func (r *Recorder) SetMode(mode string) {
	r.mu.Lock()
	r.mode = mode
	r.mu.Unlock()
	r.log.Debug("record mode set", "mode", mode)
}

// MotionActive opens/closes the persistence window in motion mode. On
// activation the pre-roll ring is flushed as the start of the new segment;
// on deactivation the current segment is closed.
func (r *Recorder) MotionActive(active bool) {
	r.mu.Lock()
	var job *marshalJob
	if r.mode != "motion" || active == r.motionActive {
		r.mu.Unlock()
		return
	}
	r.motionActive = active
	if !active {
		if r.open {
			job = r.cutLocked(time.Now())
		}
		r.mu.Unlock()
		if job != nil {
			r.marshalAndFlush(job)
		}
		return
	}
	// activation: adopt the ring as the open segment
	if !r.open && len(r.vSamples) > 0 {
		r.open = true
		r.vFirstPTS = r.vPTS[0]
		r.startNTP = r.vNTP[0]
		r.lastNTP = r.vNTP[len(r.vNTP)-1]
	}
	r.mu.Unlock()
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

// marshalJob carries the raw per-track sample lists out of the recorder lock
// so the fMP4 marshal (CPU-bound, ~ms per segment) doesn't block incoming
// packets on the next camera in the burst cut window.
type marshalJob struct {
	seq    uint32
	start  time.Time
	end    time.Time
	v      []*fmp4.Sample
	vPTS   []int64
	vFirst int64
	a      []*fmp4.Sample
	aPTS   []int64
	aFirst int64
	aRate  int64
}

// WriteVideo appends a video access unit. pts is in 90 kHz units; ntp is the
// wall-clock time of the AU (from RTCP mapping or arrival time fallback).
func (r *Recorder) WriteVideo(au [][]byte, pts int64, ntp time.Time) error {
	randAcc := r.isRandomAccess(au)

	r.mu.Lock()

	if r.mode == "off" {
		r.mu.Unlock()
		return nil
	}

	if r.mode == "motion" && !r.motionActive {
		// ring buffer: keep the last preRollRing of decodable GOPs, never persist
		avcc, err := h264.AVCC(au).Marshal()
		if err != nil {
			r.mu.Unlock()
			return err
		}
		r.vSamples = append(r.vSamples, &fmp4.Sample{
			IsNonSyncSample: !randAcc,
			Payload:         avcc,
		})
		r.vPTS = append(r.vPTS, pts)
		r.vNTP = append(r.vNTP, ntp)
		r.trimRingLocked()
		r.mu.Unlock()
		return nil
	}

	var job *marshalJob
	if !r.open {
		if !randAcc {
			r.mu.Unlock()
			return nil // wait for an IDR to open a segment
		}
		r.open = true
		r.vFirstPTS = pts
		r.startNTP = ntp
	} else if randAcc && ntp.Sub(r.startNTP) >= targetSegDur {
		// cut before this IDR: it starts the next segment. Snapshot the old
		// segment under the lock; marshal + Put + Insert all happen in a
		// goroutine so the next packet isn't blocked on encoding + IO.
		job = r.cutForNewSegmentLocked(ntp)
		r.open = true
		r.vFirstPTS = pts
		r.startNTP = ntp
	}

	avcc, err := h264.AVCC(au).Marshal()
	if err != nil {
		r.mu.Unlock()
		return err
	}
	r.vSamples = append(r.vSamples, &fmp4.Sample{
		IsNonSyncSample: !randAcc,
		Payload:         avcc,
	})
	r.vPTS = append(r.vPTS, pts)
	r.lastNTP = ntp
	r.mu.Unlock()

	if job != nil {
		go r.marshalAndFlush(job)
	}
	return nil
}

// trimRingLocked bounds the motion-mode ring to preRollRing, cutting only
// at GOP boundaries (drops everything before the newest keyframe at or
// older than the cutoff) so the ring always starts decodable.
func (r *Recorder) trimRingLocked() {
	latest := r.vNTP[len(r.vNTP)-1]
	if latest.Sub(r.vNTP[0]) <= preRollRing {
		return
	}
	cutoff := latest.Add(-preRollRing)
	cut := 0
	for i, t := range r.vNTP {
		if t.After(cutoff) {
			break
		}
		if !r.vSamples[i].IsNonSyncSample { // keyframe — safe new head
			cut = i
		}
	}
	if cut > 0 {
		r.vSamples = r.vSamples[cut:]
		r.vPTS = r.vPTS[cut:]
		r.vNTP = r.vNTP[cut:]
	}
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
	var job *marshalJob
	if r.open {
		job = r.cutLocked(time.Now())
	}
	r.mu.Unlock()

	if job != nil {
		r.marshalAndFlush(job)
	}
}

// cutForNewSegmentLocked hands the open segment's samples off to the marshal
// pipeline and resets per-segment state. Caller must hold r.mu and have
// already decided a new segment starts here (state for the new segment is
// set by the caller). The returned marshalJob is what gets sent to a marshal
// goroutine; the marshal + Put + Insert happen outside the recorder lock.
func (r *Recorder) cutForNewSegmentLocked(nextSegStart time.Time) *marshalJob {
	r.open = false
	return r.snapshotLocked(nextSegStart)
}

// cutLocked is Close's variant — drains the open segment using the given
// fallback end time. Caller must hold r.mu.
func (r *Recorder) cutLocked(nextSegStart time.Time) *marshalJob {
	r.open = false
	return r.snapshotLocked(nextSegStart)
}

// snapshotLocked copies the open segment's per-track samples into a marshal
// job and resets per-segment state. The fMP4 marshal happens off-lock in
// marshalAndFlush so the next packet for this camera isn't blocked waiting
// for the encoder.
// Caller must hold r.mu.
func (r *Recorder) snapshotLocked(nextSegStart time.Time) *marshalJob {
	defer func() {
		r.vSamples, r.vPTS, r.vNTP = nil, nil, nil
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

	r.seq++
	return &marshalJob{
		seq:    r.seq,
		start:  start,
		end:    end,
		v:      r.vSamples,
		vPTS:   r.vPTS,
		vFirst: r.vFirstPTS,
		a:      r.aSamples,
		aPTS:   r.aPTS,
		aFirst: r.aFirstPTS,
		aRate:  r.audioRate,
	}
}

// marshalAndFlush marshals the snapshot into fMP4 and persists it. Runs
// outside the recorder lock so the next packet isn't blocked on the encoder
// or storage IO. Called from a goroutine spawned by WriteVideo / Close /
// MotionActive.
func (r *Recorder) marshalAndFlush(job *marshalJob) {
	tracks := []*fmp4.PartTrack{{
		ID:       1,
		BaseTime: baseTime(job.start, videoClock),
		Samples:  job.v,
	}}
	if len(job.a) > 0 && job.aRate > 0 {
		tracks = append(tracks, &fmp4.PartTrack{
			ID:       2,
			BaseTime: baseTime(job.start, job.aRate),
			Samples:  job.a,
		})
	}

	part := &fmp4.Part{SequenceNumber: job.seq, Tracks: tracks}
	buf := &seekablebuffer.Buffer{}
	if err := part.Marshal(buf); err != nil {
		r.log.Warn("segment marshal failed", "err", err)
		return
	}

	key := storage.SegmentKey(r.camID, job.start)
	size := int64(buf.Len())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := r.backend.Put(ctx, key, bytes.NewReader(buf.Bytes()), size); err != nil {
		r.log.Warn("segment store failed", "key", key, "err", err)
		return
	}
	if err := r.store.InsertSegment(ctx, r.camID, job.start, job.end, key, size); err != nil {
		_ = r.backend.Delete(ctx, key)
		r.log.Warn("segment index failed", "key", key, "err", err)
	}
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
