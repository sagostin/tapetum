# Tapetum NVR — 08: Frontend (Vue 3)

## Stack

| Piece | Choice |
|---|---|
| Framework | Vue 3 + TypeScript (Composition API, `<script setup>`) |
| Build | Vite |
| State | Pinia |
| Router | Vue Router (auth guards, role-based route gating) |
| Playback | hls.js (+ native HLS on Safari) |
| Live | native `RTCPeerConnection` (WebRTC), `<img>` MJPEG fallback |
| Timeline | custom `<canvas>` component (no chart lib) |
| HTTP | fetch wrapper w/ CSRF header injection + 401→login redirect |
| Realtime | native WebSocket client with auto-reconnect |
| Styling | CSS custom properties; dark-first (it's a monitoring app) |

Dev: `vite dev` proxies `/api` and `/ws` to `localhost:8080`.
Prod: Go embeds `web/dist` and serves it; SPA fallback for non-API routes.

## Project Layout

```
web/
├── src/
│   ├── main.ts / App.vue
│   ├── router/              # routes + guards
│   ├── stores/              # auth, cameras, events, system (Pinia)
│   ├── api/                 # typed client per domain (auth.ts, cameras.ts, …)
│   ├── components/
│   │   ├── CameraTile.vue       # grid cell: WebRTC/MJPEG + status overlay
│   │   ├── LivePlayer.vue       # WebRTC primary, fallback chain
│   │   ├── HlsPlayer.vue        # playback wrapper around hls.js
│   │   ├── TimelineScrubber.vue # canvas: density + events + coverage + playhead
│   │   ├── PtzPad.vue           # directional pad + zoom slider + presets
│   │   ├── ZoneEditor.vue       # draw motion zones on a snapshot
│   │   └── EventCard.vue        # snapshot, label chips, clip link, ack
│   ├── views/
│   │   ├── SetupView.vue        # first-run wizard
│   │   ├── LoginView.vue
│   │   ├── DashboardView.vue    # live grid
│   │   ├── CameraView.vue       # single cam: live + PTZ + imaging + stats
│   │   ├── PlaybackView.vue     # HLS + timeline + export
│   │   ├── EventsView.vue       # filterable feed
│   │   └── admin/
│   │       ├── UsersView.vue    # users, roles, per-camera ACL matrix
│   │       ├── CamerasView.vue  # CRUD + discover + probe
│   │       ├── NotificationsView.vue
│   │       ├── StorageView.vue  # usage, retention, S3
│   │       └── SystemView.vue   # health, stats, audit log, settings
│   └── lib/                 # webrtc.ts, ws.ts, time.ts, format.ts
```

## Routes & Guards

| Path | View | Required perm |
|---|---|---|
| `/setup` | SetupView | `needs_setup` |
| `/login` | LoginView | none |
| `/` | DashboardView | live |
| `/cameras/:id` | CameraView | live (+ ACL) |
| `/cameras/:id/playback` | PlaybackView | playback (+ ACL) |
| `/events` | EventsView | events |
| `/admin/*` | admin views | respective `:write` perms |

Router guard fetches `auth/me` once, caches in Pinia; routes check perms
client-side (UX only — server enforces for real).

## Key Views

### Dashboard (live grid)

- Auto layout: 1/4/6/9/12 tiles; drag to reorder, per-user layout persisted.
- Each tile: WebRTC sub-stream; falls back to MJPEG after 2 failed attempts;
  shows status badge (● online / ▲ degraded / ○ offline), name, clock.
- Click → CameraView. Live event toasts via WS.

### Playback (the money view)

```
┌──────────────────────────────────────────────┐
│                 video (hls.js)               │
├──────────────────────────────────────────────┤
│ timeline (canvas, full width)                │
│ ▓▓▓░░▓▓ gap ▓▓▓▓░░░▓▓   ← density heatmap    │
│ ▲person ▲motion        ← event markers       │
│        │playhead (draggable)                 │
│ [◀ day] 2026-08-27 [day ▶]  [⤓ export clip]  │
└──────────────────────────────────────────────┘
```

**TimelineScrubber.vue mechanics:**

1. On range change: one `GET /recordings/timeline?buckets=<pixelWidth>`.
2. Canvas draws: coverage mask (grey gaps), density as green intensity,
   events as colored pips (red = person etc.).
3. Drag playhead → compute ts → `GET /playback/{id}/playlist.m3u8?start=ts`
   → hand to hls.js → first frame in <300ms on LAN (segment = one 6s chunk).
4. Hover → thumbnail popup (`thumbs/<segmentId>.jpg`).
5. Drag-select a range → `POST /exports` → toast + download when WS says done.
6. Zoom: buckets re-fetched at finer granularity (day → hour → 10min).

### Events feed

- Filters: camera multi-select, type, AI label chips, date range, unacked.
- Card: snapshot with drawn bbox, camera, time, label+confidence, buttons:
  play clip (inline HLS), download, ack.
- Live prepend on WS `event.created`.

### Admin — Users

- Table + create/edit modal: role dropdown (matrix shown inline), per-camera
  ACL as a checkbox matrix (users × cameras) when any user is restricted.
- API tokens panel per user (own tokens) + admin view of all.

### Admin — Cameras

- "Discover" button → scans LAN (ONVIF) → one-click adopt (fills URLs from
  profiles, prompts for credentials).
- "Test connection" (probe) before save: shows resolved streams, PTZ caps.
- Per-camera tabs: Connection / Recording (mode, retention, tiering) /
  Motion (ZoneEditor on live snapshot) / AI (labels, confidence).

### Admin — Storage

Per-camera bars: local vs S3 bytes, retention settings, projected days at
current bitrate; global disk pressure warnings from `storage.warning` events.

## Realtime Wiring

Single WS connection (`/ws`) with topic subscriptions:

| Topic | UI reaction |
|---|---|
| `camera.status` | tile badge, camera page banner |
| `event.created` | toast + events feed prepend + timeline pip |
| `export.done` | toast with download link |
| `storage.warning` | admin banner |

Reconnect: exponential backoff, resync state via REST on reconnect.

## Performance Budgets

- Initial load: <150KB JS gz (route-level code splitting; hls.js lazy-loaded
  on playback routes only).
- Dashboard with 12 tiles: sub-streams only; main stream never in grid.
- Timeline interactions: <16ms draw per frame (canvas, precomputed buckets).
- No virtual scrolling needed until events > 1k/page (cursor pagination).
