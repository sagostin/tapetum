# Tapetum NVR — 07: Detection, Events & Notifications

## Detection Pipeline

```
sub-stream ─▶ decode (ffmpeg helper / pure-Go JPEG) ─▶ downscale (≤640px)
    ─▶ [motion engine] ─▶ motion ticks ─▶ event state machine ─▶ Event
                                    │
                                    ▼ (phase 4, on event frames only)
                              [AI inference: ONNX] ─▶ labels + bboxes ─▶ Event
```

Detection runs on the **sub-stream** to keep CPU low. AI inference runs only
on frames from active motion events, never continuously.

## Phase 3: Motion Detection

Pure-Go frame differencing on downscaled luma frames (~5fps):

1. Decode sub-stream frame → grayscale → Gaussian blur.
2. Background model: rolling average (`cv`-style accumulate weighted).
3. Diff → threshold → morphological open/close → contour area sum.
4. Score = changed-area fraction; compare against `sensitivity`.

Per-camera `motion_config`:

```json
{
  "enabled": true,
  "sensitivity": 0.6,
  "min_area_pct": 0.5,
  "zones": [
    {"name": "driveway", "polygon": [[0.1,0.3],[0.9,0.3],[0.9,0.9],[0.1,0.9]], "mode": "include"},
    {"name": "street",   "polygon": [[0,0],[1,0],[1,0.2],[0,0.2]],            "mode": "exclude"}
  ],
  "schedule": {"mon": [["00:00","23:59"]], "…": []},
  "pre_roll_s": 5, "post_roll_s": 10, "cooldown_s": 30
}
```

Event state machine: `idle → (score>threshold for 3 consecutive ticks) →
active → (score<threshold for post_roll) → closed`. One `events` row per
active window, `end_ts` filled on close. Segments overlapping the window are
marked `has_motion=true`.

### Camera-side ONVIF events

onvif-go's Event service is not yet implemented, so camera-native motion
events are supported via a **small custom WS-Notification pull-point client**
(`PullMessages` against the camera's event endpoint) — reusing onvif-go's
SOAP plumbing. Cameras that support it get hardware motion events as an
additional signal source (OR'ed with software detection). If neither is
available, software detection alone carries the camera.

## Phase 4: AI Object Detection

- Runtime: ONNX (`onnxruntime_go`) with a YOLO-class model (ships with a
  small general model; user-replaceable). Optional remote inference endpoint
  (e.g. a Frigate-style detector or OpenVINO service) via config.
- Trigger: only frames within motion events (1–2fps sampling), plus optional
  "verify" pass on the event's best frame at full-res.
- Output: `label`, `confidence`, `bbox` on the event row; bboxes rendered
  onto the snapshot. Labels filterable in the event feed and timeline.
- `ai_config` per camera: `{enabled, labels: ["person","car"], min_confidence,
  schedule, max_fps}`.
- Volume note: AI ticks are exactly the workload ClickHouse is for — raw
  detections go to `detections`, UI-visible alerts remain in Postgres events.

## Events

Shape (see `02-data-model.md` for DDL):

```json
{
  "id": "…", "camera_id": "…", "type": "ai",
  "ts": "…", "end_ts": "…",
  "label": "person", "confidence": 0.87, "bbox": {"x":0.31,"y":0.12,"w":0.18,"h":0.44},
  "snapshot_url": "/api/v1/events/…/snapshot.jpg",
  "clip": {"start": "…", "end": "…", "playlist": "/api/v1/events/…/clip.m3u8"}
}
```

- Clip range = event window + pre/post roll; played via on-demand HLS,
  exportable as MP4.
- Events pin their segments (`protected=true`) until deleted or unpinned.
- Ack flow for shared households/teams: `acked_by`, `acked_at`.
- New events broadcast over WS → UI badge/feed updates live.

## Notifications

### Channels

| Type | Config | Notes |
|---|---|---|
| `smtp` | host, port, from, to[], TLS mode | Snapshot attached inline |
| `webhook` | url, method, headers, body template | Generic JSON POST; HMAC signing optional |
| `ntfy` | server, topic, priority, token | Easiest phone push |
| `gotify` | server, token, priority | Self-hosted push |
| `discord` | webhook url | Snapshot embeds natively |
| `slack` | webhook url | Blocks + image |
| `telegram` | bot token, chat id | sendPhoto with snapshot |

Secrets in `config` are encrypted at rest; channels support **test send**
from the UI.

### Rules

```json
{
  "name": "Front door people at night",
  "camera_ids": ["…"],
  "event_types": ["ai"],
  "labels": ["person"],
  "schedule": {"everyday": [["21:00","07:00"]]},
  "cooldown_s": 300,
  "channel_ids": ["…ntfy…", "…email…"]
}
```

- Matching: camera ∈ set (empty = all) AND type ∈ types AND (labels empty OR
  label ∈ labels) AND now ∈ schedule AND cooldown elapsed for
  (rule, camera).
- Delivery is async with retry (3 attempts, backoff); every attempt logged to
  `notification_log` with status `sent|failed|cooldown_skip`.
- Payload: text summary, snapshot image (where the channel supports it), and
  a deep link `https://<instance>/events/{id}` (requires `server.public_url`).

### Notification content example

> **Tapetum: Person detected — Front Door**
> 21:14:03 · confidence 87% · [View event] [Snapshot attached]

## Event-Driven Internals

All of the above rides the in-process event bus (`internal/events`):

```go
bus.Publish(Event{Topic: "ai.detected", CameraID: id, Payload: det})
// subscribers: event store writer, notify worker, WS broadcaster,
//              segment protector, recording has_motion marker
```

Single-process pub/sub is sufficient for the single-binary deployment shape;
the interface keeps the door open to NATS/Redis if multi-node ever happens.
