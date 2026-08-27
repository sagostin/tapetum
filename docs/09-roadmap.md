# Tapetum NVR — 09: Roadmap

Phased delivery — every phase ends with a usable system. No big-bang
waterfall. Phases build on each other; within a phase, tasks are roughly
ordered.

## Phase 0 — Skeleton

**Goal:** bootable app with auth and an empty dashboard.

- [ ] Go module hygiene: rename module to a real path (e.g.
      `github.com/<org>/tapetum`), `cmd/tapetumd` entrypoint
- [ ] Config loader (YAML + env overrides): server, db, storage, analytics
- [ ] Postgres wiring: pgx pool, goose migrations, migration 0001 (users,
      sessions, tokens, settings)
- [ ] Auth: argon2id, sessions, CSRF, login/logout/me, rate-limited login
- [ ] RBAC middleware + role matrix
- [ ] First-run wizard API (`/setup`) + admin creation
- [ ] API scaffolding: router, error envelope, audit log writer
- [ ] Vue 3 scaffold: Vite, Pinia, router + guards, login + setup views,
      API client with CSRF
- [ ] WS hub skeleton
- [ ] Dockerfile + docker-compose (tapetumd + postgres)

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

- [ ] ONVIF: WS-Discovery scan, adopt flow, profile sync (onvif-go)
- [ ] ONVIF: PTZ (move/stop/presets) + imaging get/set; PtzPad + Zone-less
      camera settings UI
- [ ] WebRTC live (pion): signaling route, peer management, grid upgrade
      from MJPEG → WebRTC with fallback chain
- [ ] S3 backend: put/presign/delete, per-camera tiering worker
- [ ] Storage admin UI: usage, tiering, retention projections
- [ ] H.265→H.264 lazy transcode fallback for unsupported browsers
- [ ] API tokens UI; audit log UI

**Exit:** discover + adopt an ONVIF camera in <1 min; PTZ works; segments
older than N days play back from S3 with no visible difference.

## Phase 3 — Motion Events & Notifications

**Goal:** the NVR tells you when things happen.

- [ ] Motion engine on sub-stream: frame diff, zones, sensitivity, schedules
- [ ] Event state machine → `events` rows + snapshots (full-res from main
      stream) + segment protection
- [ ] ONVIF pull-point client for camera-native motion events (custom SOAP;
      onvif-go Event service gap)
- [ ] `record_mode: motion` (pre-roll ring buffer)
- [ ] Events API + feed UI + event detail (snapshot, clip player, ack)
- [ ] Timeline density (Postgres buckets) + event markers on scrubber
- [ ] Notify worker: smtp, webhook, ntfy, gotify, discord, slack, telegram;
      rules engine (schedule + cooldown); test-send; delivery log UI
- [ ] WS `event.created` toasts

**Exit:** walk in front of a camera → motion event with snapshot → phone
notification within seconds → tap through to the clip.

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

**Exit:** 1.0.

## Cross-Cutting (every phase)

- Migrations are additive; every phase ships its own migration file.
- API changes update `03-api.md`; schema changes update `02-data-model.md`.
- Each phase ends with a soak test on real hardware with ≥2 real cameras.
- Docs in `docs/` are the contract — code follows docs, not vice versa.
