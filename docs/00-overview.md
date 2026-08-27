# Tapetum NVR — 00: Overview

> Tapetum NVR is a self-hosted, multi-user Network Video Recorder with a web UI.
> Go backend, Vue 3 frontend, Postgres metadata, optional ClickHouse analytics,
> local + S3 object storage for recordings.

## Vision

A modern NVR that is:

- **Easy to run** — one Go binary + Postgres, or one `docker compose up`.
- **Easy to use** — live camera grid, fast timeline scrubbing, event feed with
  snapshots, notifications that actually arrive.
- **Easy on hardware** — codec-copy recording (no transcode), sub-streams for
  grid/detection, CPU near zero at idle.
- **Extensible** — clean seams for motion detection, AI object detection,
  storage backends, and notification channels.

## Goals

| Area | Goal |
|---|---|
| Cameras | ONVIF discovery/adoption + generic RTSP, PTZ, imaging control |
| Recording | Continuous, 6s fMP4 chunks, per-camera retention, local + S3 |
| Live view | WebRTC (sub-second), MJPEG/HLS fallback, multi-camera grid |
| Playback | On-demand HLS generated from segment index; instant scrubbing |
| Detection | Motion detection (zones/schedules), AI object detection later |
| Events | Snapshots + clips on motion/AI events, filterable feed |
| Notifications | Email, webhooks, ntfy/Gotify, Discord/Slack/Telegram; rules with cooldowns |
| Users | Multi-user, RBAC roles, per-camera ACLs, audit log |
| Storage | Local hot tier, optional S3 sync/tiering, retention janitor |
| Analytics | Optional ClickHouse for high-volume detection/analytics |

## Non-Goals (for now)

- Cloud-hosted / multi-tenant SaaS (single-instance, self-hosted only).
- Camera firmware management beyond ONVIF basics.
- Edge recording ON the cameras (Profile G pull) — possible future phase.
- Mobile native apps (responsive web UI instead).

## Key Technology Choices

| Component | Choice | Why |
|---|---|---|
| Language | Go 1.26 | Single binary, concurrency model fits N-camera pipelines |
| RTSP ingest | `bluenviron/gortsplib` | MediaMTX core; per-packet NTP timestamps → wall-clock alignment; H.264/H.265/AV1; ONVIF backchannel |
| ONVIF | `0x524a/onvif-go` | Discovery, media profiles, PTZ, imaging (200+ APIs). Note: Event service not yet implemented — custom pull-point needed for camera-side events |
| ffmpeg | `golift/ffmpeg` wrapper | Clip exports, snapshots, transcode fallback (HEVC→H.264) |
| WebRTC | `pion/webrtc` | Sub-second live view |
| HLS | on-demand playlists (own generator) or `bluenviron/gohlslib` | Playback from recorded segments without duplicate storage |
| Primary DB | Postgres (pgx + goose migrations) | System of record: users, cameras, segment index, events |
| Analytics DB | ClickHouse (optional module) | High-volume detection events, heatmaps, long-term analytics |
| Frontend | Vue 3 + TypeScript + Vite + Pinia | hls.js for playback, native RTCPeerConnection for WebRTC |
| Object storage | S3-compatible (`aws-sdk-go-v2` or `minio-go`) | AWS S3, MinIO, Backblaze B2, etc. |

## Glossary

- **Segment** — one ~6s fMP4 file of recorded footage; the atomic unit of
  storage, playback, and retention.
- **Main stream / Sub stream** — camera's high-res and low-res RTSP profiles.
  Main is recorded; sub is used for grid view, motion detection, thumbnails.
- **Segment index** — Postgres table mapping (camera, time range) → segment
  file. Drives playlists, timeline, retention.
- **Event** — a detection (motion/AI) or system occurrence with optional
  snapshot + clip reference.
- **Tiering** — moving older segments from local disk to S3 while keeping
  them playable.

## Doc Index

| Doc | Contents |
|---|---|
| `00-overview.md` | This file |
| `01-architecture.md` | System diagram, components, data flow |
| `02-data-model.md` | Postgres schema, optional ClickHouse schema |
| `03-api.md` | REST / WebSocket / WebRTC API surface |
| `04-auth-rbac.md` | Users, roles, permissions, sessions, tokens |
| `05-ingest-streaming.md` | RTSP ingest, recording, live view, playback |
| `06-storage.md` | Chunk layout, local disk, S3 tiering, retention |
| `07-detection-notifications.md` | Motion, AI roadmap, events, notifications |
| `08-frontend.md` | Vue app structure, pages, timeline component |
| `09-roadmap.md` | Phased delivery plan with exit criteria |
