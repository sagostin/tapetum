// Package ingest runs the per-camera RTSP workers: connect, decode RTP to
// access units, feed the recorder and snapshot cache, track health, and
// update live camera status. See docs/05-ingest-streaming.md.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/sagostin/tapetum/internal/camera"
	"github.com/sagostin/tapetum/internal/live"
	"github.com/sagostin/tapetum/internal/record"
	"github.com/sagostin/tapetum/internal/storage"
	"github.com/sagostin/tapetum/internal/ws"
)

// ErrNoSnapshot means no keyframe has been decoded yet for the camera.
var ErrNoSnapshot = errors.New("no snapshot available yet")

// Supervisor manages one camera worker per enabled camera.
type Supervisor struct {
	cams    *camera.Store
	segs    *record.Store
	backend storage.Backend
	hub     *ws.Hub
	live    *live.Hub
	log     *slog.Logger

	mu      sync.Mutex
	workers map[string]*cameraWorker
	snaps   map[string]*snapshotSet // camera ID → per-stream keyframe sources
}

func NewSupervisor(cams *camera.Store, segs *record.Store, backend storage.Backend,
	hub *ws.Hub, liveHub *live.Hub,
) *Supervisor {
	return &Supervisor{
		cams:    cams,
		segs:    segs,
		backend: backend,
		hub:     hub,
		live:    liveHub,
		log:     slog.With("component", "ingest"),
		workers: map[string]*cameraWorker{},
		snaps:   map[string]*snapshotSet{},
	}
}

// Start launches workers for all enabled cameras.
func (s *Supervisor) Start(ctx context.Context) error {
	cams, err := s.cams.ListEnabled(ctx)
	if err != nil {
		return err
	}
	for _, c := range cams {
		s.addLocked(c)
	}
	s.log.Info("ingest started", "cameras", len(cams))
	return nil
}

// Sync adds, restarts, or removes the worker for a camera after a CRUD
// change. Safe to call for any camera row state. Teardown of the previous
// worker happens asynchronously so a stuck session loop (up to ~60s of
// reconnect backoff) cannot stall the API request that triggered the sync.
func (s *Supervisor) Sync(ctx context.Context, camID string) {
	s.mu.Lock()
	old := s.workers[camID]
	delete(s.workers, camID)
	delete(s.snaps, camID)
	s.mu.Unlock()

	if old != nil {
		old.stop()
	}

	cam, err := s.cams.Get(ctx, camID)
	if err != nil || !cam.Enabled {
		return
	}
	s.mu.Lock()
	if _, taken := s.workers[camID]; !taken {
		s.addLocked(cam)
	}
	s.mu.Unlock()
}

// Remove stops and drops the worker for a deleted camera. Teardown is async
// to keep Remove latency off the API hot path; subsequent calls referencing
// this camera return "not found" regardless.
func (s *Supervisor) Remove(camID string) {
	s.mu.Lock()
	old := s.workers[camID]
	delete(s.workers, camID)
	delete(s.snaps, camID)
	s.mu.Unlock()
	if old != nil {
		old.stop()
	}
}

func (s *Supervisor) addLocked(c *camera.Camera) {
	snap := newSnapshotSet()
	s.snaps[c.ID] = snap
	w := newCameraWorker(c, s.cams, s.segs, s.backend, s.hub, s.live, snap, s.log)
	s.workers[c.ID] = w
	go w.run()
}

// Close stops all workers (daemon shutdown).
func (s *Supervisor) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.workers {
		w.stop()
	}
	s.workers = map[string]*cameraWorker{}
}

// MotionSignal flips the motion-recording window for a camera
// (record_mode=motion). Called from the event-bus bridge in main.
func (s *Supervisor) MotionSignal(camID string, active bool) {
	s.mu.Lock()
	w, ok := s.workers[camID]
	s.mu.Unlock()
	if ok {
		w.recorder.MotionActive(active)
	}
}

// Stats returns live health stats for a camera.
func (s *Supervisor) Stats(camID string) map[string]any {
	s.mu.Lock()
	w, ok := s.workers[camID]
	s.mu.Unlock()
	if !ok {
		return map[string]any{"running": false}
	}
	return w.stats()
}

// WorkerCount returns the number of running camera workers.
func (s *Supervisor) WorkerCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.workers)
}

// Snapshot returns the latest decoded JPEG for a camera at full resolution
// (main stream preferred, sub fallback) — used for event snapshots and the
// /cameras/:id/snapshot endpoint. Results are cached briefly; decoding runs
// through the ffmpeg helper with singleflight + a global concurrency cap.
func (s *Supervisor) Snapshot(ctx context.Context, camID string) ([]byte, error) {
	s.mu.Lock()
	snaps, ok := s.snaps[camID]
	s.mu.Unlock()
	if !ok {
		return nil, ErrNoSnapshot
	}
	jpg, err := snaps.main.jpeg(ctx, 0)
	if errors.Is(err, ErrNoSnapshot) {
		return snaps.sub.jpeg(ctx, 0)
	}
	return jpg, err
}

// mjpegFrameWidth bounds MJPEG tile frames — tiles are a few hundred px wide,
// so decoding/encoding beyond this wastes CPU and bandwidth.
const mjpegFrameWidth = 960

// MJPEGFrame returns a scaled JPEG for the MJPEG live fallback (sub stream
// preferred — much cheaper to decode than an 8MP main keyframe — main
// fallback for cameras without a sub stream).
func (s *Supervisor) MJPEGFrame(ctx context.Context, camID string) ([]byte, error) {
	s.mu.Lock()
	snaps, ok := s.snaps[camID]
	s.mu.Unlock()
	if !ok {
		return nil, ErrNoSnapshot
	}
	jpg, err := snaps.sub.jpeg(ctx, mjpegFrameWidth)
	if errors.Is(err, ErrNoSnapshot) {
		return snaps.main.jpeg(ctx, mjpegFrameWidth)
	}
	return jpg, err
}

// formatDuration renders d compactly for status_detail (e.g. "1h23m").
func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}
