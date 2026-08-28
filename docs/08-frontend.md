# Tapetum NVR — 08: Frontend (Vue 3)

## Stack

| Piece | Choice |
|---|---|
| Framework | Vue 3 + TypeScript (Composition API, `<script setup>`) |
| Build | Vite |
| State | Pinia |
| Router | Vue Router (auth guards, role-based route gating) |
| Playback | hls.js (+ native HLS on Safari), bare `<video>` (no native controls) |
| Live | hls.js (`MediaSource` / MSE) over `/streams/{cam}/live.m3u8`, `<img>` MJPEG fallback |
| Timeline | shared `ZoomableTimeline.vue` (DOM-based, no chart lib) |
| Player zoom/pan | CSS `transform: translate() scale()` composable (`useZoomPan`) |
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
│   ├── composables/         # reusable Vue composables (useZoomPan, …)
│   ├── components/
│   │   ├── CameraTile.vue       # grid cell: HLS + status overlay
│   │   ├── LivePlayer.vue       # hls.js primary, MJPEG fallback
│   │   ├── VideoPlayer.vue      # UI3-style player: bare video + CSS-zoom + custom controls
│   │   ├── ZoomableTimeline.vue # wheel-zoom, drag-pan, click-seek, event pips
│   │   ├── PtzPad.vue           # directional pad + zoom slider + presets
│   │   ├── ZoneEditor.vue       # draw motion zones on a snapshot
│   │   └── EventCard.vue        # snapshot, label chips, clip link, ack
│   ├── views/
│   │   ├── SetupView.vue        # first-run wizard
│   │   ├── LoginView.vue
│   │   ├── DashboardView.vue    # live grid
│   │   ├── CameraView.vue       # single cam: live + PTZ + imaging + stats
│   │   ├── PlaybackView.vue     # single cam: VideoPlayer + ZoomableTimeline
│   │   ├── PlaybackAggregateView.vue  # UI3-style: tabs + single player + combined timeline
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
| `/playback` | PlaybackAggregateView | playback |
| `/playback/:cameraId` | PlaybackView | playback (+ ACL) |
| `/events` | EventsView | events |
| `/admin/*` | admin views | respective `:write` perms |

Router guard fetches `auth/me` once, caches in Pinia; routes check perms
client-side (UX only — server enforces for real).

## Key Views

### Dashboard (live grid)

- Auto layout: 1/4/6/9/12 tiles; drag to reorder, per-user layout persisted.
- Each tile: hls.js over the camera's live m3u8; falls back to MJPEG if HLS errors.
  shows status badge (● online / ▲ degraded / ○ offline), name, clock.
- Click → CameraView. Live event toasts via WS.

### Playback — Aggregate view (`/playback`)

The primary playback surface. UI3-style: a single video pane at the top
showing whichever camera the user is focused on, with an aggregate
combined timeline at the bottom that shows recordings from **all**
cameras at once.

```
┌──────────────────────────────────────────────────────────┐
│  [grandpa1] [motion-cam] [cam-3]   ← camera tabs        │
├──────────────────────────────────────────────────────────┤
│                                                          │
│                  video (one camera)                      │
│                                                          │
├──────────────────────────────────────────────────────────┤
│ 8/27 14:00 → 8/27 17:00  [Reset]   [Overlay][Stacked]    │
│ ▓▓▓▓░░▓▓▓░░▓▓▓▓▓▓▓  ← combined timeline (color per cam) │
│  ▲person  ▲motion                  ← event pips         │
│        │ playhead                                        │
└──────────────────────────────────────────────────────────┘
```

- Camera tabs above the player: click to load that camera. ←/→ to cycle.
- Aggregate timeline below: color-coded segments per camera. Click a
  segment to load that camera at that time into the player; click a pip
  to jump straight to that event; double-click to open the dedicated
  single-camera page.
- Scroll-wheel on the timeline zooms around the cursor (15 min ↔ 7 day);
  drag side-to-side pans.
- In playback mode the VideoPlayer's own control bar is shown
  (play/pause, ±10 s, scrubber, time, zoom buttons, fullscreen, reset).

### Playback — Single-camera view (`/playback/:cameraId`)

Same VideoPlayer + ZoomableTimeline components, focused on one camera.
Used when drilling down via "Open {camera} →" or double-clicking the
aggregate timeline.

### `VideoPlayer.vue`

UI3-style player wrapper. A bare `<video>` (no native controls) sits
inside a CSS-transform container so we can digital-zoom and pan around
the footage independently of the underlying stream.

- **Zoom**: scroll-wheel anywhere on the video zooms around the cursor
  (trackpad pinch = `ctrlKey+wheel`). `+` / `−` keys, zoom buttons in the
  control bar. Cursor switches to `grab` when zoomed, `grabbing` while
  panning.
- **Pan**: drag with the pointer to pan when zoomed in (CSS transform;
  bounded so you can't fly off the frame).
- **Control bar** (visible in playback mode): play/pause, ±10 s, scrubber
  with click/drag seek, current time / duration, zoom group
  (`−` / `Fit` / `+` / `⤢`), fullscreen.
- **No native controls**: deliberately hides the browser's `<video controls>`
  so dynamic src reloads and our own input handlers don't fight the
  browser UI. The user only ever sees our overlay + bar.
- **Source modes**: `mode: 'live'` (no control bar, optional LIVE badge)
  or `mode: 'playback'` (control bar shown, control bar's autoplay and
  seek-bias are off — user must press play).

### `ZoomableTimeline.vue`

Shared horizontal scrubber for all timelines.

- **Click**: emit `seek(timeMs)` (and `select(timeMs, camId?)` for the
  aggregate row's camera-finding logic).
- **Drag**: pan the window — emits `window-change(fromMs, toMs)` as the
  user drags so the parent can refetch timelines at the new range.
- **Wheel**: zoom around the cursor (15 min ↔ 7 day).
- **Double-click**: emit `open(timeMs, camId?)` — the parent uses this to
  navigate to the dedicated single-camera page.
- **Pip click**: emit `select(timeMs, camId)` — drills straight into that
  event.
- `aggregate` mode renders one combined row with per-segment hue; `stacked`
  mode (in the aggregate view) renders one row per camera with the
  default color.

### Events feed

- Filters: camera multi-select, type, AI label chips, date range, unacked.
- Card: snapshot with drawn bbox, camera, time, label+confidence, buttons:
  play clip (inline VideoPlayer), download, ack.
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
- Timeline interactions: <16ms draw per frame (the new ZoomableTimeline
  is DOM-based, no canvas; revisit if perf becomes a concern).
- No virtual scrolling needed until events > 1k/page (cursor pagination).

