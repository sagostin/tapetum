// Package storage abstracts segment storage backends. Local disk is the
// write path; S3 is an optional cold tier (phase 2). See docs/06-storage.md.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// ObjectInfo describes a stored object.
type ObjectInfo struct {
	Size int64
}

// Backend is the storage contract from docs/06-storage.md.
type Backend interface {
	// Put writes an object atomically (tmp file + rename + fsync on local).
	Put(ctx context.Context, key string, r io.Reader, size int64) error
	// Open reads an object. Local only — S3 reads use Presign.
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	// Presign returns a temporary download URL (S3 only; phase 2).
	Presign(ctx context.Context, key string, ttl time.Duration) (string, error)
	// Delete removes an object. Missing objects are not an error.
	Delete(ctx context.Context, key string) error
	// Stat returns object metadata.
	Stat(ctx context.Context, key string) (ObjectInfo, error)
}

// ErrDiskFull is returned by Put when free space drops below the safety valve.
var ErrDiskFull = errors.New("storage: disk free space below minimum")

// SegmentKey builds the canonical key for a recording segment:
// recordings/{cameraID}/{YYYY}/{MM}/{DD}/{HH}/{startUnixMs}.m4s
func SegmentKey(cameraID string, start time.Time) string {
	return fmt.Sprintf("recordings/%s/%04d/%02d/%02d/%02d/%d.m4s",
		cameraID,
		start.Year(), start.Month(), start.Day(), start.Hour(),
		start.UnixMilli())
}

// ExportKey builds the key for an export artifact.
func ExportKey(exportID string) string {
	return fmt.Sprintf("exports/%s.mp4", exportID)
}
