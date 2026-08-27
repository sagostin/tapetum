# Tapetum NVR — 06: Storage

## Overview

Two backends behind one interface; local disk is always the write path, S3 is
an optional cold tier. The Postgres `recording_segments` index tracks where
every segment physically lives.

```go
type Backend interface {
    Put(ctx context.Context, key string, r io.Reader, size int64) error
    Open(ctx context.Context, key string) (io.ReadCloser, error) // local
    Presign(ctx context.Context, key string, ttl time.Duration) (string, error) // s3
    Delete(ctx context.Context, key string) error
    Stat(ctx context.Context, key string) (ObjectInfo, error)
}
```

## Object Layout

Same key structure locally and in S3 — tiering is a move, not a transform.

```
recordings/{cameraID}/{YYYY}/{MM}/{DD}/{HH}/{startUnixMs}.m4s
snapshots/{cameraID}/{YYYY}/{MM}/{DD}/{eventID}.jpg
thumbs/{segmentID}.jpg
exports/{exportID}.mp4                      (local only, short-lived)
```

- Hour-prefix directories keep per-directory file counts sane
  (600 segments/hour max at 6s segments).
- Segment filenames embed start time → keys are self-describing and sortable.

## Local Backend

- Root: `data/recordings` (configurable).
- Writes: `*.tmp` + rename (crash-safe); fsync before index commit.
- Served via sendfile with `Range` support and `Cache-Control: immutable`.
- Disk safety valve: refuse new segments at <2% free; emit `storage.warning`
  event at configurable thresholds (default 10% and 5%).

## S3 Backend

- Any S3-compatible store (AWS, MinIO, B2, R2, Wasabi). Config: endpoint,
  region, bucket, prefix, access keys (encrypted at rest), optional storage
  class (e.g. `GLACIER_IR`).
- **Reads never proxy through Go**: `/segments/{id}` returns
  `302 + presigned GET` (15-min TTL). Browser downloads straight from S3 with
  full Range support → hls.js scrubbing stays fast.
- Multipart not needed (segments are 1–5MB) — simple `PutObject`.
- Optional: keep hot N days locally and mirror-delete (see tiering).

## Tiering

Per camera: `tier_after_days` (null = never tier).

```
tiering worker (every 15 min):
  SELECT segments WHERE camera has tiering
    AND start_ts < now() - tier_after_days
    AND storage = 'local'
  for each (bounded batch):
    PutObject(key, local file)  → verify size/etag
    UPDATE recording_segments SET storage='s3', path=key
    DELETE local file
```

Playback is transparent — playlists reference segment IDs, the `/segments`
route resolves location at request time. The timeline doesn't care where
bytes live.

## Retention Janitor

Per camera: `retention_days` (default 14) and/or `retention_gb`.

```
janitor (every 5 min per camera, round-robin):
  1. expired := segments older than retention_days, oldest first
  2. over_cap := segments exceeding retention_gb budget, oldest first
  3. skip protected = true (pinned by events or manual protect)
  4. delete object → delete index row → delete thumb
  5. write audit/system event if eviction rate spikes
```

- Deletion is object-first, then row; a reconcile job (hourly) removes
  orphans in both directions (row without object, object without row).
- Retention runs independently per tier (S3 lifecycle rules are NOT relied
  on — the index must stay authoritative).

## Motion-Only Recording

When `record_mode = motion`, the recorder holds a 10s pre-roll ring buffer in
memory; on motion start it begins persisting segments, on motion end +
post-roll it stops. Storage cost drops ~10–100× depending on scene activity.
Continuous recording remains the default (detection gaps never lose footage).

## Capacity Planning (rough guide, H.264 4MP @ 2Mbps)

| Cameras | 14 days continuous | Segment count/day/cam |
|---|---|---|
| 4 | ~1.2 TB | 14,400 |
| 8 | ~2.4 TB | 14,400 |
| 16 | ~4.8 TB | 14,400 |

The index row per segment is ~100 bytes → 16 cameras × 14 days ≈ 3.2M rows —
trivial for Postgres with the `(camera_id, start_ts)` index.

## Backups

- **Metadata**: nightly `pg_dump` of config tables (users, cameras, rules,
  index) to `data/backups/` (or S3 prefix) — small, fast, sufficient to
  rebuild the system even if footage is lost.
- **Footage**: left to the storage layer (S3 versioning / filesystem
  snapshots); Tapetum doesn't duplicate it.
- Restore: fresh install → restore dump → point `data/recordings` at the old
  tree → reconcile job re-links everything.
