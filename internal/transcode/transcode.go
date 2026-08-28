// Package transcode lazily re-encodes H.265 segments to H.264 for browsers
// without HEVC support (docs/05-ingest-streaming.md: playback_transcode).
// Results are cached under data/transcode/ and evicted with their segments.
package transcode

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/sagostin/tapetum/internal/record"
	"github.com/sagostin/tapetum/internal/storage"
)

// ErrBusy means the transcode pool is saturated; caller should retry.
var ErrBusy = errors.New("transcode pool is busy, retry shortly")

// Service transcodes and caches fMP4 fragments.
type Service struct {
	dir string
	sem chan struct{}
	log *slog.Logger

	mu       sync.Mutex
	inflight map[string]chan error // segment ID → wait channel
}

func NewService(dataDir string) (*Service, error) {
	dir := filepath.Join(dataDir, "transcode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	// 4 slots: the recorder cuts at ~1s segments now (UI3-style low live
	// latency), so H.265 → H.264 segment transcodes arrive at the rate of
	// (h265-cameras × viewers) per second. Two concurrent viewers per H.265
	// camera fit comfortably; more fall through to ErrBusy (the client
	// retries naturally since the segment will be cached on the next hit).
	return &Service{
		dir:      dir,
		sem:      make(chan struct{}, 4),
		log:      slog.With("component", "transcode"),
		inflight: map[string]chan error{},
	}, nil
}

// segmentPath is the cache location of a transcoded fragment.
func (s *Service) segmentPath(segID string) string {
	return filepath.Join(s.dir, segID+".m4s")
}

// InitPath is the cache location of a camera's transcoded init segment.
func (s *Service) InitPath(camID string) string {
	return filepath.Join(s.dir, camID+".tinit.mp4")
}

// Evict drops the cached fragment of a deleted segment.
func (s *Service) Evict(segID string) {
	_ = os.Remove(s.segmentPath(segID))
}

// EvictCamera drops the cached transcoded init of a camera.
func (s *Service) EvictCamera(camID string) {
	_ = os.Remove(s.InitPath(camID))
}

// Segment returns the transcoded fragment for seg, building it on a cache
// miss. Concurrent misses for the same segment share one build.
func (s *Service) Segment(ctx context.Context, seg *record.Segment, initBytes []byte,
	resolve storage.Resolver,
) (string, error) {
	out := s.segmentPath(seg.ID)
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}

	s.mu.Lock()
	if wait, ok := s.inflight[seg.ID]; ok {
		s.mu.Unlock()
		select {
		case err := <-wait:
			if err != nil {
				return "", err
			}
			return out, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	wait := make(chan error, 1)
	s.inflight[seg.ID] = wait
	s.mu.Unlock()

	err := s.build(ctx, seg, initBytes, resolve)
	wait <- err
	s.mu.Lock()
	delete(s.inflight, seg.ID)
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	return out, nil
}

func (s *Service) build(ctx context.Context, seg *record.Segment, initBytes []byte,
	resolve storage.Resolver,
) error {
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return ctx.Err()
	default:
		return ErrBusy
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	// combined input: init + segment fragment
	b, err := resolve(ctx, seg.Storage)
	if err != nil {
		return err
	}
	rc, err := b.Open(ctx, seg.Path)
	if err != nil {
		return err
	}
	defer rc.Close()

	tmpIn, err := os.CreateTemp(s.dir, "in-*.fmp4")
	if err != nil {
		return err
	}
	defer os.Remove(tmpIn.Name())
	if _, err := tmpIn.Write(initBytes); err != nil {
		tmpIn.Close()
		return err
	}
	if _, err := io.Copy(tmpIn, rc); err != nil {
		tmpIn.Close()
		return err
	}
	if err := tmpIn.Close(); err != nil {
		return err
	}

	// The recorder's timeline uses absolute-unix tfdt at a 90 kHz timescale;
	// ffmpeg's muxer rebases to 0, so we force the same timescale and patch
	// tfdt back to the segment's absolute position afterwards — transcoded
	// fragments slot into playlists exactly like untranscoded ones.
	tmpOut := s.segmentPath(seg.ID) + ".tmp"
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-loglevel", "error", "-y",
		"-i", tmpIn.Name(),
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "23",
		"-c:a", "copy",
		"-video_track_timescale", "90000",
		"-f", "mp4", "-movflags", "frag_keyframe+empty_moov+default_base_moof",
		tmpOut)
	var errb limitedBuffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		os.Remove(tmpOut)
		return fmt.Errorf("ffmpeg transcode: %w (%s)", err, errb.String())
	}

	// split the output at the first moof box: head = init (ftyp+moov),
	// tail = the media fragment (with tfdt patched to the absolute timeline)
	if err := s.splitInit(tmpOut, seg); err != nil {
		os.Remove(tmpOut)
		return err
	}
	return os.Rename(tmpOut, s.segmentPath(seg.ID))
}

// splitInit extracts the init portion (everything before the first moof) of
// path into the camera's tinit cache, truncating path to the fragment part
// with tfdt rewritten to the segment's absolute-unix position (90 kHz).
func (s *Service) splitInit(path string, seg *record.Segment) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	off := firstBoxOffset(data, "moof")
	if off <= 0 {
		return errors.New("transcode: no moof box in ffmpeg output")
	}
	initPath := s.InitPath(seg.CameraID) + ".tmp"
	if err := os.WriteFile(initPath, data[:off], 0o644); err != nil {
		return err
	}
	if err := os.Rename(initPath, s.InitPath(seg.CameraID)); err != nil {
		return err
	}
	frag := data[off:]
	if err := patchTFDT(frag, uint64(seg.Start.UnixMilli())*90); err != nil {
		return err
	}
	return os.WriteFile(path, frag, 0o644)
}

// patchTFDT rewrites every tfdt box in a fragment to baseTime (90 kHz).
// ffmpeg writes tfdt version 1 (8-byte value) — in-place patch, same size.
func patchTFDT(data []byte, baseTime uint64) error {
	found := false
	for off := 0; off+8 <= len(data); {
		size := int(binary.BigEndian.Uint32(data[off : off+4]))
		typ := string(data[off+4 : off+8])
		hdr := 8
		if size == 1 {
			if off+16 > len(data) {
				return errors.New("transcode: truncated largesize box")
			}
			size = int(binary.BigEndian.Uint64(data[off+8 : off+16]))
			hdr = 16
		}
		if size < hdr || off+size > len(data) {
			return errors.New("transcode: malformed box")
		}
		if typ == "moof" {
			if err := patchTFDTInBox(data[off+hdr:off+size], baseTime, &found); err != nil {
				return err
			}
		}
		off += size
	}
	if !found {
		return errors.New("transcode: no tfdt box in fragment")
	}
	return nil
}

func patchTFDTInBox(box []byte, baseTime uint64, found *bool) error {
	for off := 0; off+8 <= len(box); {
		size := int(binary.BigEndian.Uint32(box[off : off+4]))
		typ := string(box[off+4 : off+8])
		if size < 8 || off+size > len(box) {
			return errors.New("transcode: malformed inner box")
		}
		if typ == "traf" {
			inner := box[off+8 : off+size]
			for i := 0; i+8 <= len(inner); {
				isize := int(binary.BigEndian.Uint32(inner[i : i+4]))
				ityp := string(inner[i+4 : i+8])
				if isize < 8 || i+isize > len(inner) {
					return errors.New("transcode: malformed traf child")
				}
				if ityp == "tfdt" {
					if inner[i+8] != 1 {
						return errors.New("transcode: tfdt version 1 expected")
					}
					binary.BigEndian.PutUint64(inner[i+12:i+20], baseTime)
					*found = true
				}
				i += isize
			}
		}
		off += size
	}
	return nil
}

// firstBoxOffset returns the byte offset of the first box of the given type.
func firstBoxOffset(data []byte, typ string) int {
	off := 0
	for off+8 <= len(data) {
		size := int(binary.BigEndian.Uint32(data[off : off+4]))
		boxType := string(data[off+4 : off+8])
		hdr := 8
		if size == 1 {
			if off+16 > len(data) {
				return -1
			}
			size = int(binary.BigEndian.Uint64(data[off+8 : off+16]))
			hdr = 16
		}
		if boxType == typ {
			return off
		}
		if size < hdr {
			return -1
		}
		off += size
	}
	return -1
}

type limitedBuffer struct {
	b [4096]byte
	n int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if l.n < len(l.b) {
		l.n += copy(l.b[l.n:], p)
	}
	return len(p), nil
}

func (l *limitedBuffer) String() string { return string(l.b[:l.n]) }
