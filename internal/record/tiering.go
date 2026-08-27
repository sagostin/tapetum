package record

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/sagostin/tapetum/internal/camera"
	"github.com/sagostin/tapetum/internal/storage"
)

// Tierer moves segments older than each camera's tier_after_days from local
// disk to S3 (docs/06-storage.md). Playback stays transparent — the index
// tracks location and /segments presigns S3 reads.
type Tierer struct {
	segs  *Store
	cams  *camera.Store
	local storage.Backend
	s3m   *storage.S3Manager
	log   *slog.Logger
}

func NewTierer(segs *Store, cams *camera.Store, local storage.Backend, s3m *storage.S3Manager) *Tierer {
	return &Tierer{
		segs: segs, cams: cams, local: local, s3m: s3m,
		log: slog.With("component", "tiering"),
	}
}

// Run tiers in batches every 15 minutes until ctx is done.
func (t *Tierer) Run(ctx context.Context) {
	tick := time.NewTicker(15 * time.Minute)
	defer tick.Stop()
	// run once shortly after boot so a restart doesn't wait 15 min
	t.sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			t.sweep(ctx)
		}
	}
}

func (t *Tierer) sweep(ctx context.Context) {
	s3, err := t.s3m.Backend(ctx)
	if err != nil {
		t.log.Warn("s3 backend", "err", err)
		return
	}
	if s3 == nil {
		return // S3 disabled
	}
	cams, err := t.cams.List(ctx, "")
	if err != nil {
		t.log.Warn("list cameras", "err", err)
		return
	}
	for _, cam := range cams {
		if cam.TierAfterDays == nil || *cam.TierAfterDays <= 0 {
			continue
		}
		t.sweepCamera(ctx, s3, cam)
	}
}

func (t *Tierer) sweepCamera(ctx context.Context, s3 *storage.S3, cam *camera.Camera) {
	cutoff := time.Now().Add(-time.Duration(*cam.TierAfterDays) * 24 * time.Hour)
	segs, err := t.segs.SegmentsToTier(ctx, cam.ID, cutoff, 100)
	if err != nil {
		t.log.Warn("list tierable", "camera", cam.ID, "err", err)
		return
	}
	for _, seg := range segs {
		if ctx.Err() != nil {
			return
		}
		if err := t.tierOne(ctx, s3, seg); err != nil {
			t.log.Warn("tier segment", "camera", cam.ID, "segment", seg.ID, "err", err)
			continue
		}
		t.log.Debug("tiered segment", "camera", cam.ID, "segment", seg.ID, "path", seg.Path)
	}
}

// tierOne uploads one segment, verifies size, flips the index row, then
// deletes the local copy (docs/06-storage.md: object-first ordering).
func (t *Tierer) tierOne(ctx context.Context, s3 *storage.S3, seg *Segment) error {
	rc, err := t.local.Open(ctx, seg.Path)
	if err != nil {
		return err
	}
	defer rc.Close()
	if err := s3.Put(ctx, seg.Path, rc, seg.SizeBytes); err != nil {
		return err
	}
	info, err := s3.Stat(ctx, seg.Path)
	if err != nil {
		return err
	}
	if info.Size != seg.SizeBytes {
		return fmt.Errorf("tiering: size mismatch after upload: want %d, got %d",
			seg.SizeBytes, info.Size)
	}
	if err := t.segs.MarkTiered(ctx, seg.ID); err != nil {
		return err
	}
	return t.local.Delete(ctx, seg.Path)
}
