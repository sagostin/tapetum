package record

import (
	"context"
	"log/slog"
	"time"

	"github.com/sagostin/tapetum/internal/camera"
	"github.com/sagostin/tapetum/internal/storage"
	"github.com/sagostin/tapetum/internal/ws"
)

// Janitor enforces per-camera retention (days and/or GB budget).
// See docs/06-storage.md — protected segments are never evicted.
type Janitor struct {
	segs    *Store
	cams    *camera.Store
	backend storage.Backend
	hub     *ws.Hub
	log     *slog.Logger

	lastWarn time.Time
}

func NewJanitor(segs *Store, cams *camera.Store, backend storage.Backend, hub *ws.Hub) *Janitor {
	return &Janitor{segs: segs, cams: cams, backend: backend, hub: hub,
		log: slog.With("component", "janitor")}
}

// Run evicts every minute until ctx is done.
func (j *Janitor) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			j.sweep(ctx)
		}
	}
}

func (j *Janitor) sweep(ctx context.Context) {
	camIDs, err := j.segs.CameraIDsWithSegments(ctx)
	if err != nil {
		j.log.Warn("list cameras with segments", "err", err)
		return
	}
	for _, camID := range camIDs {
		cam, err := j.cams.Get(ctx, camID)
		if err != nil {
			continue // camera deleted; cascade removed rows anyway
		}
		j.sweepCamera(ctx, cam)
	}
	j.checkDisk()
}

func (j *Janitor) sweepCamera(ctx context.Context, cam *camera.Camera) {
	// 1. age-based eviction
	if cam.RetentionDays > 0 {
		cutoff := time.Now().Add(-time.Duration(cam.RetentionDays) * 24 * time.Hour)
		j.evict(ctx, cam.ID, func(s *Segment) bool { return s.Start.Before(cutoff) })
	}
	// 2. size-budget eviction
	if cam.RetentionGB != nil && *cam.RetentionGB > 0 {
		budget := int64(*cam.RetentionGB) << 30
		total, err := j.segs.CameraBytes(ctx, cam.ID)
		if err != nil {
			return
		}
		if total > budget {
			over := total - budget
			j.evict(ctx, cam.ID, func(s *Segment) bool {
				if over <= 0 {
					return false
				}
				over -= s.SizeBytes
				return true
			})
		}
	}
}

// evict deletes oldest-first while keep(seg) is true. Object first, then row
// (docs/06-storage.md consistency rules).
func (j *Janitor) evict(ctx context.Context, camID string, while func(*Segment) bool) {
	segs, err := j.segs.ListEvictable(ctx, camID, 500)
	if err != nil {
		j.log.Warn("list evictable", "camera", camID, "err", err)
		return
	}
	for _, s := range segs {
		if !while(s) {
			return
		}
		if s.Storage != "local" {
			continue // S3 eviction lands with the S3 backend (phase 2)
		}
		if err := j.backend.Delete(ctx, s.Path); err != nil {
			j.log.Warn("delete object", "path", s.Path, "err", err)
			continue
		}
		if err := j.segs.DeleteSegment(ctx, s.ID); err != nil {
			j.log.Warn("delete index row", "id", s.ID, "err", err)
		}
	}
}

// checkDisk emits a throttled storage.warning over WS at low free space.
func (j *Janitor) checkDisk() {
	l, ok := j.backend.(*storage.Local)
	if !ok {
		return
	}
	free := l.FreeFrac()
	if free < 0.10 && time.Since(j.lastWarn) > 15*time.Minute {
		j.lastWarn = time.Now()
		j.hub.Broadcast("storage.warning", map[string]any{
			"free_frac": free,
			"message":   "recording disk is nearly full",
		})
	}
}
