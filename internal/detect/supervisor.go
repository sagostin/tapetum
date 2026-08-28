package detect

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/sagostin/tapetum/internal/camera"
	"github.com/sagostin/tapetum/internal/events"
	"github.com/sagostin/tapetum/internal/live"
)

// Supervisor runs one motion engine per camera whose motion_config is
// enabled. Engine transitions are published to the event bus
// ("motion.started"/"motion.ended") for the events manager, recorder
// (record_mode=motion) and notification worker.
type Supervisor struct {
	cams *camera.Store
	live *live.Hub
	bus  *events.Bus
	log  *slog.Logger

	mu      sync.Mutex
	engines map[string]*Engine
}

func NewSupervisor(cams *camera.Store, hub *live.Hub, bus *events.Bus) *Supervisor {
	return &Supervisor{
		cams:    cams,
		live:    hub,
		bus:     bus,
		log:     slog.With("component", "detect"),
		engines: map[string]*Engine{},
	}
}

// Start launches engines for all enabled cameras with detection on.
func (s *Supervisor) Start(ctx context.Context) error {
	cams, err := s.cams.ListEnabled(ctx)
	if err != nil {
		return err
	}
	for _, c := range cams {
		s.syncOne(c)
	}
	s.log.Info("motion detection started", "engines", len(s.engines))
	return nil
}

// Sync reconciles the engine for a camera after CRUD changes.
func (s *Supervisor) Sync(ctx context.Context, camID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeLocked(camID)
	cam, err := s.cams.Get(ctx, camID)
	if err != nil || !cam.Enabled {
		return
	}
	s.syncOneLocked(cam)
}

// Remove stops the engine for a deleted camera.
func (s *Supervisor) Remove(camID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeLocked(camID)
}

// Close stops all engines (daemon shutdown).
func (s *Supervisor) Close() {
	s.mu.Lock()
	engines := s.engines
	s.engines = map[string]*Engine{}
	s.mu.Unlock()
	for _, e := range engines {
		e.Stop()
	}
}

func (s *Supervisor) syncOne(c *camera.Camera) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeLocked(c.ID)
	s.syncOneLocked(c)
}

func (s *Supervisor) syncOneLocked(c *camera.Camera) {
	cfg := ParseConfig(rawJSON(c.MotionConfig))
	if !cfg.Enabled || c.SubURL == nil || *c.SubURL == "" {
		return
	}
	camID := c.ID
	bus := s.bus
	eng := NewEngine(camID, cfg, s.live, Callbacks{
		OnStart: func(ts time.Time) {
			bus.Publish(events.Message{Topic: "motion.started", CameraID: camID,
				Payload: map[string]any{"ts": ts, "source": "software"}})
		},
		OnEnd: func(start, end time.Time, peak float64) {
			bus.Publish(events.Message{Topic: "motion.ended", CameraID: camID,
				Payload: map[string]any{"start": start, "end": end, "peak": peak, "source": "software"}})
		},
	})
	s.engines[c.ID] = eng
	eng.Start()
}

func (s *Supervisor) removeLocked(camID string) {
	if e, ok := s.engines[camID]; ok {
		delete(s.engines, camID)
		e.Stop()
	}
}

// rawJSON renders the camera's motion_config map back to JSON for parsing.
func rawJSON(m map[string]any) []byte {
	if m == nil {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return b
}
