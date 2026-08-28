package notify

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/sagostin/tapetum/internal/camera"
	"github.com/sagostin/tapetum/internal/events"
	"github.com/sagostin/tapetum/internal/storage"
)

// Worker matches events against rules and delivers notifications with retry.
type Worker struct {
	store     *Store
	evStore   *events.Store
	cams      *camera.Store
	bus       *events.Bus
	backend   storage.Backend
	publicURL string
	log       *slog.Logger

	mu       sync.Mutex
	lastSent map[string]time.Time // ruleID|camID → last successful dispatch
}

func NewWorker(store *Store, evStore *events.Store, cams *camera.Store,
	bus *events.Bus, backend storage.Backend, publicURL string,
) *Worker {
	return &Worker{
		store: store, evStore: evStore, cams: cams, bus: bus,
		backend: backend, publicURL: publicURL,
		log:      slog.With("component", "notify"),
		lastSent: map[string]time.Time{},
	}
}

// Run processes event.created messages until ctx is done.
func (w *Worker) Run(ctx context.Context) {
	ch, cancel := w.bus.Subscribe("event.created")
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-ch:
			e, ok := msg.Payload.(*events.Event)
			if !ok {
				continue
			}
			w.dispatch(ctx, e)
		}
	}
}

func (w *Worker) dispatch(ctx context.Context, e *events.Event) {
	rules, err := w.store.ListRules(ctx)
	if err != nil {
		w.log.Error("list rules", "err", err)
		return
	}
	for _, r := range rules {
		if !r.Enabled || !w.match(r, e) {
			continue
		}
		key := r.ID + "|" + e.CameraID
		w.mu.Lock()
		last, seen := w.lastSent[key]
		w.mu.Unlock()
		cooldown := time.Duration(r.CooldownS) * time.Second
		if seen && time.Since(last) < cooldown {
			for _, chID := range r.ChannelIDs {
				w.logDelivery(ctx, r.ID, chID, e, "cooldown_skip", nil)
			}
			continue
		}
		w.mu.Lock()
		w.lastSent[key] = time.Now()
		w.mu.Unlock()
		for _, chID := range r.ChannelIDs {
			go w.deliver(r.ID, chID, e)
		}
	}
}

// match applies camera/type/label/schedule filters.
func (w *Worker) match(r *Rule, e *events.Event) bool {
	if len(r.CameraIDs) > 0 && !contains(r.CameraIDs, e.CameraID) {
		return false
	}
	if len(r.EventTypes) > 0 && !contains(r.EventTypes, e.Type) {
		return false
	}
	if len(r.Labels) > 0 && (e.Label == nil || !contains(r.Labels, *e.Label)) {
		return false
	}
	return scheduleActive(r.Schedule, e.Ts)
}

// deliver sends with retry (3 attempts: immediate, 5s, 20s) and logs the
// final outcome.
func (w *Worker) deliver(ruleID, chID string, e *events.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	ch, err := w.store.GetChannelForSend(ctx, chID)
	if err != nil || ch == nil || !ch.Enabled {
		w.logDelivery(ctx, ruleID, chID, e, "failed", strPtr("channel unavailable/disabled"))
		return
	}

	payload := w.buildPayload(ctx, e)
	backoffs := []time.Duration{0, 5 * time.Second, 20 * time.Second}
	var lastErr error
	for i, wait := range backoffs {
		if wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
		// snapshot attaches asynchronously — refetch on retries
		if i > 0 && len(payload.Snapshot) == 0 {
			payload.Snapshot = w.snapshotBytes(ctx, e.ID)
		}
		if err := Send(ctx, ch, payload); err != nil {
			lastErr = err
			continue
		}
		w.logDelivery(ctx, ruleID, chID, e, "sent", nil)
		_ = w.evStore.MarkNotified(ctx, e.ID)
		return
	}
	w.logDelivery(ctx, ruleID, chID, e, "failed", strPtr(lastErr.Error()))
	w.log.Warn("delivery failed", "channel", ch.Name, "event", e.ID, "err", lastErr)
}

// buildPayload renders the notification content (docs/07 example).
func (w *Worker) buildPayload(ctx context.Context, e *events.Event) *Payload {
	camName := e.CameraID
	if cam, err := w.cams.Get(ctx, e.CameraID); err == nil && cam != nil {
		camName = cam.Name
	}
	kind := "Motion"
	if e.Type == "ai" && e.Label != nil {
		kind = strings.Title(*e.Label) + " detected"
	}
	title := fmt.Sprintf("Tapetum: %s — %s", kind, camName)
	text := e.Ts.Local().Format("15:04:05")
	if e.Confidence != nil {
		text += fmt.Sprintf(" · confidence %.0f%%", *e.Confidence*100)
	}
	url := ""
	if w.publicURL != "" {
		url = strings.TrimRight(w.publicURL, "/") + "/events?id=" + e.ID
	}
	return &Payload{
		Title:    title,
		Text:     text,
		URL:      url,
		Snapshot: w.snapshotBytes(ctx, e.ID),
	}
}

// snapshotBytes reads the event snapshot from local storage (may not exist
// yet — the caller retries).
func (w *Worker) snapshotBytes(ctx context.Context, eventID string) []byte {
	rc, err := w.backend.Open(ctx, storage.SnapshotKey(eventID))
	if err != nil {
		return nil
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, 8<<20))
	if err != nil {
		return nil
	}
	return b
}

// TestChannel sends a synthetic notification through a channel (UI test-send).
func (w *Worker) TestChannel(ctx context.Context, channelID string) error {
	ch, err := w.store.GetChannelForSend(ctx, channelID)
	if err != nil {
		return err
	}
	if ch == nil {
		return fmt.Errorf("channel not found")
	}
	return Send(ctx, ch, &Payload{
		Title: "Tapetum: Test notification",
		Text:  "Test send from Tapetum NVR at " + time.Now().Format(time.RFC3339),
		URL:   w.publicURL,
	})
}

func (w *Worker) logDelivery(ctx context.Context, ruleID, chID string, e *events.Event, status string, errMsg *string) {
	entry := &LogEntry{
		RuleID:    &ruleID,
		ChannelID: &chID,
		EventTs:   &e.Ts,
		EventID:   &e.ID,
		Status:    status,
		Error:     errMsg,
	}
	if err := w.store.LogDelivery(ctx, entry); err != nil {
		w.log.Warn("log delivery", "err", err)
	}
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func strPtr(s string) *string { return &s }

// scheduleActive evaluates rule schedules: {"everyday": [["21:00","07:00"]]}
// or per-weekday keys. Empty schedule = always active. Windows may wrap
// midnight.
func scheduleActive(sched map[string]any, t time.Time) bool {
	if len(sched) == 0 {
		return true
	}
	dayKeys := []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
	var windows []any
	if v, ok := sched["everyday"].([]any); ok {
		windows = v
	} else if v, ok := sched[dayKeys[int(t.Weekday())]].([]any); ok {
		windows = v
	} else {
		return false
	}
	now := t.Hour()*60 + t.Minute()
	for _, w := range windows {
		pair, ok := w.([]any)
		if !ok || len(pair) != 2 {
			continue
		}
		start, err1 := parseHM(fmt.Sprint(pair[0]))
		end, err2 := parseHM(fmt.Sprint(pair[1]))
		if err1 != nil || err2 != nil {
			continue
		}
		if start <= end {
			if now >= start && now < end {
				return true
			}
		} else if now >= start || now < end {
			return true
		}
	}
	return false
}

func parseHM(s string) (int, error) {
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil {
		return 0, err
	}
	return h*60 + m, nil
}
