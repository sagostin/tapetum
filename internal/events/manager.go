package events

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/sagostin/tapetum/internal/camera"
	"github.com/sagostin/tapetum/internal/record"
	"github.com/sagostin/tapetum/internal/storage"
	"github.com/sagostin/tapetum/internal/ws"
)

// Manager turns bus motion signals into event rows with artifacts: on
// motion.started it opens an event (snapshot, segment protection, WS
// broadcast, notify trigger); on motion.ended it closes it (end_ts, clip
// range, has_motion marking). Software detection and ONVIF pull-point
// signals are OR'ed — a start while an event is open is a no-op.
type Manager struct {
	store   *Store
	cams    *camera.Store
	segs    *record.Store
	backend storage.Backend
	ws      *ws.Hub
	bus     *Bus
	snapFn  func(ctx context.Context, camID string) ([]byte, error)
	log     *slog.Logger

	mu   sync.Mutex
	open map[string]*Event // camID → open event
}

func NewManager(store *Store, cams *camera.Store, segs *record.Store,
	backend storage.Backend, wsHub *ws.Hub, bus *Bus,
	snapFn func(ctx context.Context, camID string) ([]byte, error),
) *Manager {
	return &Manager{
		store: store, cams: cams, segs: segs, backend: backend,
		ws: wsHub, bus: bus, snapFn: snapFn,
		log:  slog.With("component", "events"),
		open: map[string]*Event{},
	}
}

// Run subscribes to motion topics until ctx is done.
func (m *Manager) Run(ctx context.Context) {
	started, stopS := m.bus.Subscribe("motion.started")
	ended, stopE := m.bus.Subscribe("motion.ended")
	defer stopS()
	defer stopE()
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-started:
			m.onStart(msg.CameraID, payloadTime(msg.Payload, "ts"))
		case msg := <-ended:
			p, _ := msg.Payload.(map[string]any)
			end, _ := p["end"].(time.Time)
			if end.IsZero() {
				end = time.Now()
			}
			peak, _ := p["peak"].(float64)
			m.onEnd(msg.CameraID, end, peak)
		}
	}
}

func payloadTime(p any, key string) time.Time {
	if m, ok := p.(map[string]any); ok {
		if t, ok := m[key].(time.Time); ok {
			return t
		}
	}
	return time.Now()
}

func (m *Manager) onStart(camID string, ts time.Time) {
	m.mu.Lock()
	if _, ok := m.open[camID]; ok {
		m.mu.Unlock()
		return // OR semantics: event already open
	}
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	preRoll := m.preRoll(ctx, camID)
	e := &Event{
		CameraID:  camID,
		Ts:        ts,
		Type:      "motion",
		ClipStart: ptrTime(ts.Add(-preRoll)),
		Metadata:  map[string]any{},
	}
	if err := m.store.Insert(ctx, e); err != nil {
		m.log.Error("event insert failed", "camera", camID, "err", err)
		return
	}

	m.mu.Lock()
	m.open[camID] = e
	m.mu.Unlock()

	// pin whatever already exists in the pre-roll window
	if _, err := m.segs.ProtectRange(ctx, camID, ts.Add(-preRoll), ts); err != nil {
		m.log.Warn("protect range", "err", err)
	}

	m.ws.Broadcast("event.created", map[string]any{
		"id": e.ID, "camera_id": camID, "type": e.Type, "ts": e.Ts,
	})
	m.bus.Publish(Message{Topic: "event.created", CameraID: camID, Payload: e})

	go m.attachSnapshot(e)
}

func (m *Manager) onEnd(camID string, end time.Time, peak float64) {
	m.mu.Lock()
	e, ok := m.open[camID]
	if ok {
		delete(m.open, camID)
	}
	m.mu.Unlock()
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := m.store.Close(ctx, e.ID, end); err != nil {
		m.log.Error("event close failed", "id", e.ID, "err", err)
		return
	}
	e.EndTs = &end
	if peak > 0 {
		e.Metadata["peak_score"] = peak
		_ = m.store.SetMetadata(ctx, e.ID, e.Metadata)
	}

	preRoll := m.preRoll(ctx, camID)
	clipStart := e.Ts.Add(-preRoll)
	if err := m.store.SetClip(ctx, e.ID, clipStart, end); err != nil {
		m.log.Warn("set clip", "err", err)
	}
	if _, err := m.segs.ProtectRange(ctx, camID, clipStart, end); err != nil {
		m.log.Warn("protect range", "err", err)
	}
	if err := m.segs.MarkMotionRange(ctx, camID, clipStart, end); err != nil {
		m.log.Warn("mark motion", "err", err)
	}
}

// attachSnapshot grabs a JPEG from the live stream and stores it.
func (m *Manager) attachSnapshot(e *Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	jpg, err := m.snapFn(ctx, e.CameraID)
	if err != nil {
		m.log.Warn("event snapshot", "id", e.ID, "err", err)
		return
	}
	key := storage.SnapshotKey(e.ID)
	if err := m.backend.Put(ctx, key, bytes.NewReader(jpg), int64(len(jpg))); err != nil {
		m.log.Warn("snapshot store", "err", err)
		return
	}
	if err := m.store.SetSnapshot(ctx, e.ID, key); err != nil {
		m.log.Warn("snapshot index", "err", err)
	}
}

// preRoll reads the camera's motion_config pre_roll_s (default 5s).
func (m *Manager) preRoll(ctx context.Context, camID string) time.Duration {
	cam, err := m.cams.Get(ctx, camID)
	if err != nil {
		return 5 * time.Second
	}
	if v, ok := cam.MotionConfig["pre_roll_s"].(float64); ok && v > 0 {
		return time.Duration(v) * time.Second
	}
	return 5 * time.Second
}

func ptrTime(t time.Time) *time.Time { return &t }
