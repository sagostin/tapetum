# Tapetum NVR — 01: Architecture

## System Diagram

```
                        ┌──────────────────────── Tapetum (Go binary) ────────────────────────┐
                        │                                                                     │
  Camera 1 ──RTSP──▶    │  ┌─────────┐   ┌──────────┐   ┌────────────┐   ┌────────────────┐  │
  Camera 2 ──RTSP──▶    │  │ Ingest  │──▶│ Recorder │──▶│  Storage   │──▶│ Local disk     │  │
  Camera N ──RTSP──▶    │  │(gortsplib│  │ (fMP4    │   │  manager   │──▶│ S3 (optional)  │  │
                        │  │ workers)│   │ segmenter│   │            │   └────────────────┘  │
     ▲                  │  └────┬────┘   │ codec-   │   └─────┬──────┘                       │
     │ ONVIF (SOAP)     │       │        │  copy)   │         │ segment index                │
     │ (onvif-go)       │       │        └──────────┘         ▼                              │
     ▼                  │       │ sub-stream           ┌────────────┐                        │
  PTZ / imaging /       │       ▼                      │  Postgres  │ (users, cameras,       │
  discovery             │  ┌──────────┐   events       │            │  segments, events)     │
                        │  │ Detect   │──────────────▶ │ ClickHouse │ (optional analytics)   │
                        │  │(motion/AI)│              └────────────┘                        │
                        │  └──────────┘                                                     │
                        │       │                                                           │
                        │  ┌───────────┐    ┌─────────────────────────┐                     │
                        │  │ Stream hub│───▶│ WebRTC live (pion)      │                     │
                        │  │           │───▶│ HLS playback (on-demand │                     │
                        │  │           │    │  playlists from index)  │                     │
                        │  └───────────┘    └─────────────────────────┘                     │
                        │       │                                                           │
                        │  ┌───────────┐    ┌────────────┐                                  │
                        │  │ API layer │───▶│ Notify     │ (smtp/webhook/ntfy/discord/...)  │
                        │  │ REST + WS │    │ worker     │                                  │
                        │  └───────────┘    └────────────┘                                  │
                        └───────┬───────────────────────────────────────────────────────────┘
                                │ HTTP(S): REST, WS, WebRTC signaling, static SPA
                                ▼
                     Browser — Vue 3 SPA
                     (live grid, timeline playback, events, admin)
```

## Components

### Ingest (`internal/ingest`)

One supervised worker per camera (per stream: main + optional sub):

- gortsplib RTSP client, TCP transport default, UDP optional.
- Auto-reconnect with exponential backoff; health stats (bitrate, fps, packet
  loss, last-seen) exposed to API.
- Reads RTP packets, extracts **NTP timestamps** → maps every frame to
  wall-clock time. This mapping is what makes the timeline accurate.
- Pushes frames to: Recorder (main), Detect (sub), Stream hub (both).

### Recorder (`internal/record`)

- Codec-copy: no transcoding. H.264/H.265 Annex-B from RTP is muxed directly
  into fMP4 via `bluenviron/mediacommon`.
- Cuts a new segment every ~6s **on a keyframe boundary** (segments must start
  with an IDR frame to be independently playable).
- On segment close: fsync, write row to `recording_segments`, hand path to
  storage manager.
- Gap handling: if ingest drops, a `recording_gaps` row marks the missing
  range so the timeline can render it honestly.

### Stream hub (`internal/stream`)

- **Live WebRTC**: pion-based; per-viewer peer connections pulling from the
  in-memory ring buffer of the sub (or main) stream. Sub-second latency.
- **Live fallback**: MJPEG from sub-stream JPEG re-encode (cheap at 5fps
  low-res), or short-latency HLS.
- **Playback HLS**: on-demand `.m3u8` generated from the Postgres segment
  index for a requested `[start, end)` range. Segments are the recorded fMP4
  files themselves — nothing is duplicated or pre-transcoded.

### Storage manager (`internal/storage`)

- Backend interface: `local` and `s3` implementations.
- Write path always lands on local disk first (hot tier).
- Tiering worker moves segments older than `tier_after_days` to S3 and
  updates the index row's location.
- Retention janitor enforces per-camera `max_days` / `max_gb`: deletes
  object + index row together, oldest first, never deletes segments referenced
  by a protected event.

### Detect (`internal/detect`)

- Phase 3: motion detection on sub-stream (decode → downscale → frame diff
  with zones/sensitivity/schedules).
- Phase 4: ONNX object detection on event frames (not continuous).
- Emits events to the event bus.

### Event bus + stores (`internal/events`)

- In-process pub/sub for `motion.started`, `motion.ended`, `ai.detected`,
  `camera.offline`, `storage.warning`, etc.
- `events.Store` interface with two implementations:
  - `postgres` (default, always present)
  - `clickhouse` (optional; when enabled, detection/telemetry events are
    dual-written or redirected — see `02-data-model.md`)

### Notify (`internal/notify`)

- Rule engine: camera(s) × event type × schedule × cooldown.
- Channels: SMTP email, generic webhook, ntfy, Gotify, Discord, Slack,
  Telegram. Payload: text + snapshot + deep link to event.

### API layer (`internal/api`)

- REST under `/api/v1`, WebSocket for live updates (camera status, event
  feed), WebRTC signaling endpoint.
- RBAC middleware on every route; audit logging for admin mutations.
- Serves embedded Vue SPA for non-API routes.

## Data Flow — Recording

```
camera RTSP ─▶ ingest worker ─▶ RTP packets (+NTP ts)
                                  │
                                  ▼
                         recorder buffers GOP
                                  │ every ~6s on keyframe
                                  ▼
                         close segment file (.m4s)
                                  │
                          INSERT recording_segments
                                  │
                    ┌─────────────┴──────────────┐
                    ▼                            ▼
             local hot tier            tiering worker (age > N)
                                             │
                                             ▼
                                     S3 + UPDATE location
```

## Data Flow — Playback (the fast-scrub path)

```
GET /api/v1/playback/{cam}/playlist.m3u8?start=…&end=…
        │
        ▼
SELECT segments WHERE camera_id=? AND start_ts < end AND end_ts > start
        │
        ▼
Generate HLS media playlist in-memory:
  #EXTINF = segment durations, URIs → /segments/{id} route
        │
        ▼
/segments/{id} resolves index row → serve from local disk
   or redirect to presigned S3 URL (with Range support)
        │
        ▼
hls.js buffers only the 6s chunks around the playhead → instant seek
```

## Data Flow — Motion Event

```
sub-stream frames ─▶ detect (frame diff in zones) ─▶ threshold crossed
                                                          │
                                  ┌───────────────────────┼────────────────────┐
                                  ▼                       ▼                    ▼
                          snapshot JPEG            event row in DB        notify worker
                          (golift/ffmpeg or        (+ ClickHouse if       (rules match →
                           sub-stream keyframe)     analytics enabled)     channels fire)
                                  │
                                  ▼
                        event.clip → segment range (+ pre/post-roll)
```

## Deployment Shapes

| Shape | Components | For |
|---|---|---|
| Single binary | tapetumd + embedded SPA, external Postgres | Home/small installs |
| docker compose | tapetumd + postgres (+ clickhouse, minio) | Recommended default |
| Full | tapetumd × N behind LB (stateless API; ingest pinned per node) | Later / larger installs |

## Key Design Decisions (and why)

1. **Codec-copy recording** — transcode-per-camera doesn't scale on consumer
   hardware; copy is ~zero CPU. Transcode only on demand (exports, HEVC
   fallback).
2. **6s fMP4 segments as the single storage unit** — one artifact serves
   archive, HLS playback, S3 sync, and retention. No duplicate HLS renditions.
3. **On-demand playlists from the index** — scrubbing to any timestamp is a
   DB query, not a file search. Buffering starts at the nearest 6s chunk.
4. **Postgres is always required; ClickHouse is an opt-in module** — analytics
   features degrade gracefully to Postgres (partitioned events table) when
   ClickHouse is absent.
5. **ONVIF Event service workaround** — onvif-go lacks Event service; camera-
   side motion events use a small custom WS pull-point client, or fall back to
   software motion detection.
