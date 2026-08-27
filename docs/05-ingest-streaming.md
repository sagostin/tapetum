# Tapetum NVR — 05: Ingest, Recording & Streaming

## Ingest Workers (`internal/ingest`)

Each enabled camera gets a supervised worker pair (main + optional sub stream):

```
┌─ camera worker (supervisor) ─────────────────────────────┐
│  main stream worker  ── gortsplib.Client (TCP default)   │
│  sub stream worker   ── gortsplib.Client                 │
│  reconnect: 1s → 2s → 5s → 15s → 60s cap, jittered       │
│  health: bitrate / fps / packet loss / last frame age    │
└──────────────────────────────────────────────────────────┘
```

- On connect: `DESCRIBE` → parse SDP → select H.264/H.265 (+ AAC/PCMA audio)
  → `SETUP` → `PLAY`.
- Every RTP packet is decoded to an access unit with **PTS + NTP wall-clock
  timestamp** (gortsplib provides both). The NTP value is what lands in
  `recording_segments.start_ts/end_ts` — camera clock skew is corrected via
  RTCP sender reports.
- Frames are fanned out in-process (zero-copy, `[]byte` + refcount):
  - main → Recorder, Stream hub (live), 
  - sub → Detect, Snapshot service, Stream hub (grid).
- Camera status transitions (`online/offline/degraded`) are written to
  `cameras.status` and pushed over WS.

## Recording (`internal/record`)

**Codec-copy into fMP4, cut on keyframes.**

- Muxer: `bluenviron/mediacommon/pkg/formats/fmp4` — no transcode. Typical
  CPU: ~1–2% per 4MP camera.
- Segment policy: close + cut when (`age >= 6s`) AND (next frame is IDR).
  Hard cap 10s if the camera's GOP is long; never cut mid-GOP.
- File written to `data/recordings/<cam>/<yyyy>/<mm>/<dd>/<hh>/<startUnix>.m4s`
  via temp-file + atomic rename, fsync before index insert.
- On close: `INSERT recording_segments` (start/end from NTP mapping, size,
  `has_motion` left false — detect worker flips it).
- `record_mode = motion` (phase 3): recorder keeps a 10s ring buffer and only
  persists segments overlapping an active motion window (+ post-roll).
- Gaps: on ingest failure a `recording_gaps` row opens; closes on reconnect.
  The timeline renders gaps explicitly.

### Why segments + index instead of hourly files or pre-baked HLS

| Need | Hourly MP4 | Pre-baked HLS | 6s fMP4 + index (chosen) |
|---|---|---|---|
| Seek to arbitrary time | slow (moov, big files) | fast | fast (DB query) |
| Storage duplication | none | 2× (archive + HLS) | none |
| S3-friendly partial reads | poor | ✅ | ✅ |
| Retention granularity | 1h | per-segment | per-6s-segment |
| Clip export | re-seek big files | re-mux | concat segments, `-c copy` |

## Live View

### Primary: WebRTC (pion)

- `POST /streams/{cam}/webrtc` with SDP offer → answer; trickle ICE over same
  route. No STUN needed on LAN; TURN optional config for remote access.
- Source: in-process ring buffer of the requested stream (default sub for
  grid, main for single view). H.264 passthrough — no transcode when the
  camera sends H.264.
- Max peers per camera (default 4) → overflow clients fall back.
- H.265-only cameras: transcode to H.264 via ffmpeg on demand (flagged
  per-camera; off by default, CPU warning in UI).

### Fallbacks

- **MJPEG** (`/streams/{cam}/mjpeg`): sub-stream keyframes/JPEG re-encode at
  ~5fps. Works everywhere, useful for embedding.
- **Near-live HLS** (`/streams/{cam}/live.m3u8`): playlist over the most
  recent N recorded segments. ~6–12s behind real time; zero extra work since
  recorder already produces the chunks.

## Playback — On-Demand HLS (fast scrubbing)

The headline feature. No pre-generated playlists; everything derives from the
Postgres segment index.

```
GET /playback/{cam}/playlist.m3u8?start=…&end=…

1. SELECT id, start_ts, end_ts, storage, path FROM recording_segments
   WHERE camera_id=$1 AND start_ts < $3 AND end_ts > $2 ORDER BY start_ts
2. Emit media playlist:
     #EXTM3U
     #EXT-X-TARGETDURATION:10
     #EXT-X-MEDIA-SEQUENCE:<first segment epoch>
     #EXTINF:6.023,
     /api/v1/segments/<uuid>
     …
3. /segments/{id} → local: sendfile with Range; s3: 302 to presigned GET
```

**Why this scrubs fast:**

- Scrub anywhere → one indexed query (~ms) → hls.js fetches only the 6s
  chunk under the playhead (~1–3MB) → renders from its leading keyframe.
- Time-to-first-frame on seek ≈ one segment download; on LAN typically <300ms.
- Segments are immutable → aggressive HTTP caching (`Cache-Control:
  immutable, max-age=1y`), browser/CDN cache friendly.
- S3-backed segments redirect to presigned URLs — the Go process never
  proxies bytes.

**H.265 playback**: Safari plays HEVC in HLS natively; Chrome/Firefox need
transcode. Per-camera setting `playback_transcode: auto|never|always` —
`auto` serves original + lazily transcodes on unsupported browsers (ffmpeg,
cached in `data/transcode/`).

## Timeline Data API

`GET /recordings/timeline?camera=&from=&to=&buckets=200`

- `density[]`: per-bucket motion intensity (0–1). Source: ClickHouse
  `motion_minutes` when enabled, else bucketed query over `events`.
- `recorded[]`: coverage mask from segment index (gaps appear as holes).
- `events[]`: markers with type/label for the scrubber ribbon.
- One request powers the whole timeline; bucket count matches pixel width.

## Snapshots & Thumbnails

- Live snapshot: latest sub-stream keyframe decoded to JPEG in-process
  (via ffmpeg helper) — `GET /cameras/{id}/snapshot` is O(1) after warmup.
- Event snapshot: full-res frame from the **main** stream at event ts
  (golift/ffmpeg `GetVideo`/`SaveVideo` pattern against the segment file,
  `-ss` seek + `-frames:v 1`).
- Timeline hover thumbnails: lazily generated per segment
  (`data/thumbs/<segmentID>.jpg`), cached, garbage-collected with segments.

## Exports

`POST /exports {camera_id, start, end}`:

1. Resolve overlapping segments from index.
2. ffmpeg concat demuxer, `-c copy` → single MP4 (fast; no re-encode).
   Re-encode only when crossing codec parameter changes (rare) — fallback
   `-c:v libx264 -preset veryfast`.
3. Progress over WS (`export.done`); download via `/exports/{id}/download`.

## Capacity Notes (defaults, tunable)

- Segment size: 6s.
- Recorder write buffer: 4MB per camera.
- Live ring buffer: ~4s main + ~4s sub per camera.
- ffmpeg helper pool: 2 concurrent (snapshots/exports/transcodes queue).
- Max WebRTC peers per camera: 4; global soft cap 32.
