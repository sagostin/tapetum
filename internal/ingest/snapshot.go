package ingest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"
)

// jpegCacheTTL coalesces decode bursts; MJPEG viewers within the window
// share one ffmpeg decode.
const jpegCacheTTL = 2 * time.Second

// decodeSem bounds concurrent ffmpeg decodes across all cameras so snapshot/
// MJPEG load can never peg the CPU regardless of viewer count.
var decodeSem = make(chan struct{}, 2)

// snapshot caches the latest keyframe (Annex-B) of one camera stream and
// decodes it to JPEG on demand via the ffmpeg helper, with a short result
// cache, singleflight, and stale-while-decoding semantics.
type snapshot struct {
	mu        sync.Mutex
	annexb    []byte
	codec     string // "h264" | "h265"
	cachedAt  time.Time
	cachedJPG []byte
	inflight  chan struct{} // non-nil while a decode runs; closed on completion
}

// snapshotSet holds the per-stream snapshots of a camera. Main and sub are
// tracked separately so a high-res main keyframe never clobbers the cheap
// sub keyframe (and vice versa / codec flip-flops).
type snapshotSet struct {
	main *snapshot
	sub  *snapshot
}

func newSnapshotSet() *snapshotSet {
	return &snapshotSet{main: &snapshot{}, sub: &snapshot{}}
}

// offer stores the AU on the matching stream when it is a keyframe.
func (ss *snapshotSet) offer(isSub bool, au [][]byte, codec string, sps, pps, vps []byte) {
	if isSub {
		ss.sub.offer(au, codec, sps, pps, vps)
		return
	}
	ss.main.offer(au, codec, sps, pps, vps)
}

// offer stores the AU when it is a keyframe (random access).
func (s *snapshot) offer(au [][]byte, codec string, sps, pps, vps []byte) {
	var isRA bool
	if codec == "h265" {
		isRA = h265.IsRandomAccess(au)
	} else {
		isRA = h264.IsRandomAccess(au)
	}
	if !isRA {
		return
	}
	// prepend parameter sets so the ffmpeg decode is self-contained
	full := make([][]byte, 0, len(au)+3)
	if codec == "h265" && len(vps) > 0 {
		full = append(full, vps)
	}
	if len(sps) > 0 {
		full = append(full, sps)
	}
	if len(pps) > 0 {
		full = append(full, pps)
	}
	full = append(full, au...)
	annexb, err := h264.AnnexB(full).Marshal()
	if err != nil {
		return
	}
	s.mu.Lock()
	s.annexb = annexb
	s.codec = codec
	s.mu.Unlock()
}

// jpeg returns a decoded JPEG of at most width pixels wide (0 = full
// resolution). One decode runs at a time per snapshot (singleflight);
// concurrent callers get the fresh cache, the in-flight result, or the last
// stale frame — never a second ffmpeg process.
func (s *snapshot) jpeg(ctx context.Context, width int) ([]byte, error) {
	s.mu.Lock()
	if len(s.cachedJPG) > 0 && time.Since(s.cachedAt) < jpegCacheTTL {
		jpg := s.cachedJPG
		s.mu.Unlock()
		return jpg, nil
	}
	if wait := s.inflight; wait != nil {
		stale := s.cachedJPG
		s.mu.Unlock()
		if len(stale) > 0 {
			return stale, nil // serve stale rather than queue a second decode
		}
		// No cache yet (first frame): wait for the in-flight decode.
		select {
		case <-wait:
			s.mu.Lock()
			defer s.mu.Unlock()
			if len(s.cachedJPG) > 0 {
				return s.cachedJPG, nil
			}
			return nil, ErrNoSnapshot
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	annexb, codec := s.annexb, s.codec
	if len(annexb) == 0 {
		s.mu.Unlock()
		return nil, ErrNoSnapshot
	}
	wait := make(chan struct{})
	s.inflight = wait
	s.mu.Unlock()

	jpg, err := decodeJPEG(ctx, annexb, codec, width)

	s.mu.Lock()
	if err == nil {
		s.cachedAt = time.Now()
		s.cachedJPG = jpg
	}
	s.inflight = nil
	close(wait)
	stale := s.cachedJPG
	s.mu.Unlock()

	if err != nil {
		if len(stale) > 0 {
			return stale, nil
		}
		return nil, err
	}
	return jpg, nil
}

// decodeJPEG runs one ffmpeg invocation: raw H264/H265 in, single MJPEG out.
// width > 0 scales the output down (cheaper encode, smaller payload for
// MJPEG tiles). A global semaphore caps concurrent decodes.
func decodeJPEG(ctx context.Context, annexb []byte, codec string, width int) ([]byte, error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, errors.New("ffmpeg not found in PATH")
	}
	select {
	case decodeSem <- struct{}{}:
		defer func() { <-decodeSem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	demux := "h264"
	if codec == "h265" {
		demux = "hevc"
	}
	args := []string{
		"-loglevel", "error",
		"-threads", "1",
		"-f", demux, "-i", "pipe:0",
	}
	if width > 0 {
		args = append(args, "-vf", fmt.Sprintf("scale=%d:-2", width))
	}
	args = append(args, "-frames:v", "1", "-f", "mjpeg", "-q:v", "4", "pipe:1")

	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "ffmpeg", args...)
	cmd.Stdin = bytes.NewReader(annexb)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg snapshot: %w (%s)", err, errb.String())
	}
	if out.Len() == 0 {
		return nil, errors.New("ffmpeg snapshot: empty output")
	}
	return out.Bytes(), nil
}
