# Tapetum NVR — 03: API Surface

Base path: `/api/v1`. JSON everywhere. Auth via session cookie (UI) or
`Authorization: Bearer <api_token>` (integrations). Every route is RBAC-gated
(see `04-auth-rbac.md`); the required permission is listed per route.

**Conventions**

- Errors: `{"error": {"code": "camera_not_found", "message": "…"}}` with
  matching HTTP status.
- Pagination: `?limit=50&cursor=<opaque>` → response has `next_cursor`.
- Time params: RFC 3339 (`from`, `to`).
- Permissions shorthand: `live`, `playback`, `ptz`, `export`, `events`,
  `cameras:write`, `users:write`, `settings:write` — each also implies a
  role tier per the matrix in `04-auth-rbac.md`.

## Setup & Auth

| Method | Path | Perm | Description |
|---|---|---|---|
| GET | `/setup/status` | none | `{needs_setup: bool}` — true on first run |
| POST | `/setup` | none (only when `needs_setup`) | Create initial admin, set instance name |
| POST | `/auth/login` | none | `{username, password}` → session cookie (HttpOnly, Secure, SameSite=Lax) |
| POST | `/auth/logout` | any | Destroy session |
| GET | `/auth/me` | any | Current user + effective permissions + camera ACL |
| POST | `/auth/password` | any | Change own password `{current, new}` |
| GET/POST | `/auth/tokens` | any | List/create own API tokens `{name, scopes, expires_at}` |
| DELETE | `/auth/tokens/{id}` | any | Revoke |

## Users & Roles (admin UI)

| Method | Path | Perm | Description |
|---|---|---|---|
| GET | `/users` | users:write | List users |
| POST | `/users` | users:write | Create `{username, display_name, email, password, role}` |
| GET | `/users/{id}` | users:write | Detail incl. camera ACL |
| PATCH | `/users/{id}` | users:write | Update fields, `{disabled}`, `{role}` |
| DELETE | `/users/{id}` | users:write | Delete (cannot delete last admin) |
| PUT | `/users/{id}/cameras` | users:write | Replace camera ACL: `{camera_ids: []}` (empty = all) |
| GET | `/roles` | users:write | Role catalog + permission matrix (static) |
| GET | `/audit-log?user=&action=&from=&to=` | users:write | Paginated audit entries |

## Cameras

| Method | Path | Perm | Description |
|---|---|---|---|
| GET | `/cameras` | live | List (filtered by caller's ACL) incl. live `status` |
| POST | `/cameras` | cameras:write | Create camera (see body below) |
| GET | `/cameras/{id}` | live | Detail |
| PATCH | `/cameras/{id}` | cameras:write | Update config, retention, motion/AI config |
| DELETE | `/cameras/{id}` | cameras:write | Delete; `?delete_recordings=true` optional |
| POST | `/cameras/{id}/enable` `/disable` | cameras:write | Start/stop ingest |
| POST | `/cameras/discover` | cameras:write | ONVIF WS-Discovery scan → `[{endpoint, name, manufacturer, …}]` |
| POST | `/cameras/probe` | cameras:write | Test connection `{url|onvif_endpoint, username, password}` → streams, profiles, PTZ caps |
| POST | `/cameras/{id}/onvif/sync` | cameras:write | Pull profiles/PTZ/imaging caps from device |
| GET | `/cameras/{id}/snapshot` | live | Live JPEG (from sub-stream keyframe or ffmpeg) |
| GET | `/cameras/{id}/stats` | live | Bitrate, fps, packet loss, uptime, storage used |

Camera create/update body:

```json
{
  "name": "Front Door",
  "main_url": "rtsp://192.168.1.50/Streaming/Channels/101",
  "sub_url": "rtsp://192.168.1.50/Streaming/Channels/102",
  "username": "admin", "password": "…",
  "transport": "tcp",
  "onvif_endpoint": "http://192.168.1.50/onvif/device_service",
  "record_mode": "continuous",
  "retention_days": 14, "retention_gb": 200, "tier_after_days": 7,
  "motion_config": {"enabled": true, "sensitivity": 0.6, "zones": [ … ]},
  "group_id": null
}
```

## PTZ & Imaging (phase 2)

| Method | Path | Perm | Description |
|---|---|---|---|
| POST | `/cameras/{id}/ptz/move` | ptz | `{pan, tilt, zoom}` continuous speeds, `{timeout_ms}` |
| POST | `/cameras/{id}/ptz/stop` | ptz | Stop all axes |
| GET | `/cameras/{id}/ptz/presets` | ptz | List presets |
| POST | `/cameras/{id}/ptz/presets` | ptz | Save current as preset `{name}` |
| POST | `/cameras/{id}/ptz/presets/{token}/goto` | ptz | Go to preset |
| DELETE | `/cameras/{id}/ptz/presets/{token}` | ptz | Delete preset |
| GET/PUT | `/cameras/{id}/imaging` | ptz | Brightness/contrast/exposure/etc. |

## Live Streaming

| Method | Path | Perm | Description |
|---|---|---|---|
| POST | `/streams/{cameraId}/webrtc` | live | WebRTC signaling: `{sdp_offer}` → `{sdp_answer}`. Trickle ICE via `PATCH` same path |
| GET | `/streams/{cameraId}/mjpeg` | live | MJPEG fallback (sub-stream) |
| GET | `/streams/{cameraId}/live.m3u8` | live | Short-window HLS of recent segments (near-live, ~10s delay) |
| WS | `/ws` | any | Push channel: `camera.status`, `event.created`, `export.done`, `storage.warning` |

WS message envelope:

```json
{"topic": "event.created", "data": {"id": "…", "camera_id": "…", "type": "motion", "ts": "…"}}
```

## Recordings & Playback

| Method | Path | Perm | Description |
|---|---|---|---|
| GET | `/recordings/timeline?camera=&from=&to=&buckets=200` | playback | Timeline heatmap (below) |
| GET | `/playback/{cameraId}/playlist.m3u8?start=&end=` | playback | On-demand HLS playlist from segment index |
| GET | `/segments/{segmentId}` | playback | Segment bytes; local → file (Range ok), S3 → 302 presigned URL |
| GET | `/recordings/availability?camera=&from=&to=` | playback | `[{start, end}]` recorded ranges + gaps |
| POST | `/exports` | export | `{camera_id, start, end}` → queued MP4 export (ffmpeg concat, `-c copy`) |
| GET | `/exports` | export | Own exports (admin: all) |
| GET | `/exports/{id}/download` | export | MP4 file |
| POST | `/segments/protect` | playback | `{camera_id, start, end}` → pin segments against retention |
| DELETE | `/segments/protect/{id}` | playback | Unpin |

Timeline response (drives the scrubber):

```json
{
  "camera_id": "…", "from": "…", "to": "…", "buckets": 200,
  "density": [0, 0.4, 1.0, 0, …],          // motion intensity 0–1 per bucket
  "recorded": [{"start": "…", "end": "…"}], // coverage mask (gaps omitted)
  "events": [{"id": "…", "ts": "…", "type": "ai", "label": "person"}]
}
```

## Events

| Method | Path | Perm | Description |
|---|---|---|---|
| GET | `/events?camera=&type=&label=&from=&to=&limit=&cursor=` | events | Paginated feed |
| GET | `/events/{id}` | events | Detail + signed snapshot URL + clip range |
| GET | `/events/{id}/snapshot.jpg` | events | Snapshot image |
| GET | `/events/{id}/clip.m3u8` | events | HLS playlist for the event clip range |
| POST | `/events/{id}/ack` | events | Acknowledge |
| DELETE | `/events/{id}` | cameras:write | Delete event (+ artifacts) |

## Notifications

| Method | Path | Perm | Description |
|---|---|---|---|
| GET/POST | `/notify/channels` | settings:write | List/create channel `{name, type, config}` |
| PATCH/DELETE | `/notify/channels/{id}` | settings:write | Update/disable/delete |
| POST | `/notify/channels/{id}/test` | settings:write | Send test notification → `{status}` |
| GET/POST | `/notify/rules` | settings:write | List/create rule |
| PATCH/DELETE | `/notify/rules/{id}` | settings:write | Update/disable/delete |
| GET | `/notify/log?rule=&status=` | settings:write | Delivery log |

## System & Admin

| Method | Path | Perm | Description |
|---|---|---|---|
| GET | `/system/health` | none | `{status: "ok"}` (liveness) |
| GET | `/system/info` | any | Version, uptime, build |
| GET | `/system/stats` | settings:write | CPU/mem, ingest totals, viewer counts |
| GET | `/system/storage` | settings:write | Per-camera bytes, per-tier usage, retention projections |
| GET/PUT | `/system/settings` | settings:write | S3 config, public URL, analytics (ClickHouse) config |
| GET | `/camera-groups` / POST / PATCH / DELETE | cameras:write | Camera group CRUD |

## Rate Limits & Safety

- Login: 5 attempts / 5 min / IP (progressive delay).
- `/cameras/probe`, `/cameras/discover`: admin/operator only, 10/min — these
  hit the LAN.
- WebRTC: max N concurrent peers per camera (config, default 4) — extra
  viewers get MJPEG/HLS fallback.
- Exports: 1 concurrent job per user, global queue cap.
