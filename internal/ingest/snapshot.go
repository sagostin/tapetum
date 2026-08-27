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

// snapshot caches the latest keyframe (Annex-B) of a camera and decodes it
// to JPEG on demand via the ffmpeg helper, with a short result cache.
type snapshot struct {
	mu        sync.Mutex
	annexb    []byte
	codec     string // "h264" | "h265"
	cachedAt  time.Time
	cachedJPG []byte
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

// jpeg returns a decoded JPEG, cached for ~1s to coalesce bursts.
func (s *snapshot) jpeg(ctx context.Context) ([]byte, error) {
	s.mu.Lock()
	if time.Since(s.cachedAt) < time.Second && len(s.cachedJPG) > 0 {
		jpg := s.cachedJPG
		s.mu.Unlock()
		return jpg, nil
	}
	annexb, codec := s.annexb, s.codec
	s.mu.Unlock()

	if len(annexb) == 0 {
		return nil, ErrNoSnapshot
	}

	jpg, err := decodeJPEG(ctx, annexb, codec)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cachedAt = time.Now()
	s.cachedJPG = jpg
	s.mu.Unlock()
	return jpg, nil
}

// decodeJPEG runs one ffmpeg invocation: raw H264/H265 in, single MJPEG out.
func decodeJPEG(ctx context.Context, annexb []byte, codec string) ([]byte, error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, errors.New("ffmpeg not found in PATH")
	}
	demux := "h264"
	if codec == "h265" {
		demux = "hevc"
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "ffmpeg",
		"-loglevel", "error",
		"-f", demux, "-i", "pipe:0",
		"-frames:v", "1", "-f", "mjpeg", "-q:v", "4", "pipe:1")
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
