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
	snaps   map[string]*snapshot // camera ID → latest keyframe JPEG source
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
		snaps:   map[string]*snapshot{},
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
// change. Safe to call for any camera row state.
func (s *Supervisor) Sync(ctx context.Context, camID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w, ok := s.workers[camID]; ok {
		w.stop()
		delete(s.workers, camID)
		delete(s.snaps, camID)
	}
	cam, err := s.cams.Get(ctx, camID)
	if err != nil || !cam.Enabled {
		return
	}
	s.addLocked(cam)
}

// Remove stops and drops the worker for a deleted camera.
func (s *Supervisor) Remove(camID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w, ok := s.workers[camID]; ok {
		w.stop()
		delete(s.workers, camID)
		delete(s.snaps, camID)
	}
}

func (s *Supervisor) addLocked(c *camera.Camera) {
	snap := &snapshot{}
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

// Snapshot returns the latest decoded JPEG for a camera (sub-stream
// preferred, main fallback). Results are cached briefly; decoding runs
// through the ffmpeg helper.
func (s *Supervisor) Snapshot(ctx context.Context, camID string) ([]byte, error) {
	s.mu.Lock()
	snap, ok := s.snaps[camID]
	s.mu.Unlock()
	if !ok {
		return nil, ErrNoSnapshot
	}
	return snap.jpeg(ctx)
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
