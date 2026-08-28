# Tapetum NVR — 09: Roadmap

Phased delivery — every phase ends with a usable system. No big-bang
waterfall. Phases build on each other; within a phase, tasks are roughly
ordered.

## Phase 0 — Skeleton

**Goal:** bootable app with auth and an empty dashboard.

- [x] Go module hygiene: rename module to a real path (e.g.
      `github.com/<org>/tapetum`), `cmd/tapetumd` entrypoint
- [x] Config loader (YAML + env overrides): server, db, storage, analytics
- [x] Postgres wiring: pgx pool, goose migrations, migration 0001 (users,
      sessions, tokens, settings)
- [x] Auth: argon2id, sessions, CSRF, login/logout/me, rate-limited login
- [x] RBAC middleware + role matrix
- [x] First-run wizard API (`/setup`) + admin creation
- [x] API scaffolding: router, error envelope, audit log writer
- [x] Vue 3 scaffold: Vite, Pinia, router + guards, login + setup views,
      API client with CSRF
- [x] WS hub skeleton
- [x] Dockerfile + docker-compose (tapetumd + postgres)

**Exit:** create admin → log in → see empty dashboard. `docker compose up`
works from clean checkout.

## Phase 1 — Core NVR

**Goal:** record and play back. The NVR is genuinely useful here.

- [x] Camera CRUD + probe (RTSP only; manual URLs)
- [x] Ingest workers (gortsplib v5, main + sub, reconnect, health stats)
- [x] Recorder: fMP4 segmenter, keyframe cutting, index writes, gap rows
- [x] Local storage backend + retention janitor (days/GB)
- [x] Near-live HLS (from recent segments) + MJPEG snapshot route
- [x] Playback: `/recordings/timeline`, on-demand `playlist.m3u8`,
      `/segments/{id}` with Range
- [x] Dashboard grid (MJPEG tiles OK this phase) + camera detail page
- [x] PlaybackView: HlsPlayer + TimelineScrubber (coverage + playhead; no
      density yet)
- [x] Clip export (ffmpeg re-mux of combined fMP4, `-c copy`) + downloads

**Exit:** one camera records 24h continuously; scrub any moment in the
timeline with <500ms to first frame; export a 1-minute MP4; retention
evicts old segments.

**Verified (E2E smoke, mediamtx + ffmpeg testsrc):** probe → create → record
(6s fMP4, keyframe cuts) → status/stats → snapshot JPEG → playlist/init/
segment+Range → timeline/availability → protect → export MP4 → RBAC →
gap open/close on stream loss/recovery → retention eviction. 24h soak on
real hardware still pending (cross-cutting requirement).

## Phase 2 — Camera Intelligence & Storage Tiers

**Goal:** ONVIF integration, proper live view, S3.

- [x] ONVIF: WS-Discovery scan, adopt flow, profile sync (onvif-go)
- [x] ONVIF: PTZ (move/stop/presets) + imaging get/set; PtzPad + Zone-less
      camera settings UI
- [x] WebRTC live (pion): signaling route, peer management, grid upgrade
      from MJPEG → WebRTC with fallback chain
- [x] S3 backend: put/presign/delete, per-camera tiering worker
- [x] Storage admin UI: usage, tiering, retention projections
- [x] H.265→H.264 lazy transcode fallback for unsupported browsers
- [x] API tokens UI; audit log UI

**Exit:** discover + adopt an ONVIF camera in <1 min; PTZ works; segments
older than N days play back from S3 with no visible difference.

**Verified (E2E smoke, mediamtx + ffmpeg testsrc + minio + pion test client):**
WS-Discovery found a real LAN device → probe/sync/PTZ/imaging error paths
clean without endpoint → WebRTC offer/answer with real pion client receiving
H.264 RTP (111 packets) → H.265 camera transcode fallback (ffmpeg, tfdt
patched to absolute timeline, verified at box level) → S3 config w/ bucket
ping → tiering worker moved a 3-day-old segment to minio → `/segments` 302
presigned GET (bytes served) → export spanning an S3 segment → janitor
evicted the S3 segment (object + row gone). Real ONVIF camera adopt/PTZ
needs physical hardware (soak, cross-cutting requirement).

## Phase 3 — Motion Events & Notifications

**Goal:** the NVR tells you when things happen.

- [x] Motion engine on sub-stream: frame diff, zones, sensitivity, schedules
- [x] Event state machine → `events` rows + snapshots (full-res from main
      stream) + segment protection
- [x] ONVIF pull-point client for camera-native motion events (custom SOAP;
      onvif-go Event service gap)
- [x] `record_mode: motion` (pre-roll ring buffer)
- [x] Events API + feed UI + event detail (snapshot, clip player, ack)
- [x] Timeline density (Postgres buckets) + event markers on scrubber
- [x] Notify worker: smtp, webhook, ntfy, gotify, discord, slack, telegram;
      rules engine (schedule + cooldown); test-send; delivery log UI
- [x] WS `event.created` toasts

**Exit:** walk in front of a camera → motion event with snapshot → phone
notification within seconds → tap through to the clip.

**Implemented (code-complete, E2E smoke pending):** motion engine w/
zones/sensitivity/schedules + state machine → events + full-res snapshots +
segment protection; ONVIF pull-point; record_mode=motion pre-roll ring;
events feed w/ live `event.created` prepend + inline clip player + ack;
timeline density buckets + event markers; notify worker w/ all 7 senders +
rules + test-send + delivery log UI; global toasts (AppShell). Still owed:
E2E smoke paragraph (like Phases 1–2) + real-hardware soak (cross-cutting
requirement).

## Phase 4 — AI Detection & Analytics

**Goal:** know *what* moved, not just *that* something moved.

- [ ] ONNX inference service (onnxruntime_go), bundled YOLO-class model,
      event-triggered sampling
- [ ] Labels/confidence/bbox on events; bbox overlay on snapshots
- [ ] Label filters in events feed, timeline markers, notification rules
- [ ] `events.Store` seam → ClickHouse module: `detections` table +
      `motion_minutes` MV; density queries switch backend by config
- [ ] Analytics view: events per camera/day/label charts (ClickHouse when
      enabled, Postgres fallback)
- [ ] Remote inference endpoint option (external detector service)

**Exit:** filter the feed to `person` only; "person at front door at night"
rule works; timeline heatmap over 30 days loads instantly with ClickHouse
enabled.

## Phase 5 — Hardening & Polish

**Goal:** release candidate.

- [ ] OIDC login + role mapping; TOTP 2FA
- [ ] Two-way audio (ONVIF backchannel via gortsplib)
- [ ] Camera groups + group-scoped dashboards
- [ ] Backup/restore (metadata dump + reconcile)
- [ ] Multi-language strings (i18n scaffolding)
- [ ] Perf pass: 16-camera soak test, memory profile, janitor/tiering load
- [ ] Security pass: SSRF guards, header audit, dependency audit, rate-limit
      review
- [ ] Docs: install guide, reverse-proxy guide, S3 guide, FAQ
- [ ] Playback UI overhaul (UI3-style) — see notes below

### Playback UI overhaul (in-flight)

`/playback` and `/playback/:cameraId` are converging on a single UI3-style
shape: one camera-at-a-time player area at the top with full digital
zoom/pan, plus a zoomable/pan-able combined timeline at the bottom that
shows recordings from every camera at once (color-coded per camera).

- [x] Shared `VideoPlayer.vue` — bare `<video>` in a CSS-transform
      container, custom control bar, no native browser controls.
- [x] Digital zoom/pan composable (`useZoomPan`) reused everywhere.
- [x] Shared `ZoomableTimeline.vue` — wheel-zoom + drag-pan + click-seek
      + double-click-to-open + pip-click-to-event.
- [x] Aggregate view (`/playback`) with camera tabs, mode toggle
      (overlay / stacked), and click-to-drill.
- [x] Single-camera view (`/playback/:cameraId`) uses the same player +
      timeline primitives.
- [ ] Camera groups in the aggregate view (Phase 5 work; rows/timeline
      should fold into a single chip and timeline row when group scope
      is selected).
- [ ] Density-heatmap buckets backend (Phase 3 territory but rendered in
      the new timeline today via Postgres; ClickHouse accelerates at
      Phase 4).
- [ ] Hover thumbnail popup on the timeline (UI3 seek hint).
- [ ] Pinch-to-zoom on touch devices.
- [ ] Export-range drag-select on the timeline (drag a span → POST /exports).
- [ ] Keyboard map (UI3 parity: `space` play/pause, `←`/`→` ±10 s in
      playback or cycle cams in live, `F` fullscreen, `0` reset zoom,
      `+/-` zoom).

**Exit:** 1.0.

## Cross-Cutting (every phase)

- Migrations are additive; every phase ships its own migration file.
- API changes update `03-api.md`; schema changes update `02-data-model.md`.
- Each phase ends with a soak test on real hardware with ≥2 real cameras.
- Docs in `docs/` are the contract — code follows docs, not vice versa.
