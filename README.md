# Tapetum NVR

A self-hosted, multi-user Network Video Recorder with a web UI. Go backend,
Vue 3 frontend, Postgres metadata, optional S3 cold tier — designed to run on
a single node, from a home lab box to a small office server.

- **Codec-copy recording** — H.264/H.265 is muxed straight into ~6s fMP4
  segments. No transcoding on the record path, so CPU stays near zero at idle.
- **Sub-second live view** — WebRTC (pion) with MJPEG/HLS fallback.
- **Instant timeline scrubbing** — playback HLS playlists are generated
  on-demand from the Postgres segment index; the browser only buffers the 6s
  chunks around the playhead.
- **ONVIF** — WS-Discovery, camera adoption, profile sync, PTZ, imaging
  control. Generic RTSP URLs work too.
- **Storage tiers** — local hot tier with per-camera retention (days/GB),
  optional S3-compatible cold tier (AWS, MinIO, Backblaze B2, ...) that stays
  playable via presigned URLs.
- **Multi-user** — RBAC roles, per-camera ACLs, API tokens, audit log.
- **Single binary** — the Vue SPA is embedded; one `tapetumd` + Postgres is
  the whole system.

## Project status

Tapetum is in active development. Working today (verified E2E): camera
CRUD/probe, continuous recording, retention janitor, live view
(WebRTC/MJPEG), timeline playback, clip export, ONVIF discovery/PTZ, S3
tiering, users/RBAC/audit. On the roadmap: motion detection and
notifications (Phase 3), AI object detection and ClickHouse analytics
(Phase 4). See [`docs/09-roadmap.md`](docs/09-roadmap.md).

## Quick start (Docker Compose — recommended)

The compose stack is the intended single-node deployment: app + Postgres,
one command. All deploy-time settings live in `.env` — you never need to
edit `docker-compose.yml`.

```sh
git clone <repo-url> && cd tapetum
cp .env.example .env   # edit: TAPETUM_DEV, POSTGRES_PASSWORD, ...
docker compose up --build -d
```

Then open **http://localhost:8080** and complete the first-run setup wizard
to create your admin account.

Two named volumes hold all state:

| Volume | Contents |
|---|---|
| `pgdata` | Postgres: users, cameras, segment index, events, settings |
| `tapetum-data` | Recordings, transcode cache, `server.key` (secret encryption key) |

ffmpeg is already bundled in the app image (used for clip exports,
snapshots, and the HEVC→H.264 transcode fallback).

## Homelab deployment (LAN IP + port)

The common case: Tapetum runs on a home server and you open
`http://<server-ip>:8080` from other machines on the LAN.

**One setting is mandatory:** `TAPETUM_DEV=true`. Session cookies carry the
`Secure` flag unless dev mode is on, and browsers silently drop Secure
cookies over plain HTTP on any address other than localhost — so without
this, login will appear to work but immediately log you out. The name is
misleading; think of it as "plain-HTTP mode". It's the default in
`.env.example`, so a fresh `.env` is already LAN-ready:

```sh
# .env
TAPETUM_DEV=true
```

With that set, the stock deployment works as-is on the LAN (`APP_PORT` is
published on all interfaces). Changing `POSTGRES_PASSWORD`, setting
`TAPETUM_SECRET_KEY`, and sizing retention from the production section below
still apply; `TAPETUM_PUBLIC_URL` only matters once you go external.

**WebRTC on LAN in Docker:** in the default bridge network the app
advertises container-internal ICE candidates, so browsers on other LAN
machines usually can't establish WebRTC and fall back to MJPEG/HLS
automatically. On a Linux host you can get real WebRTC by running the app
with `network_mode: host` (then set
`TAPETUM_DATABASE_URL=postgres://...@localhost:5432/...` in `.env`).

### Optional: external access

In order of preference:

1. **VPN / mesh (Tailscale, WireGuard)** — zero exposure. Access stays
   plain HTTP over the tunnel, so keep `TAPETUM_DEV=true`. With Tailscale
   you can also use `tailscale serve` to get a real HTTPS cert on your
   tailnet name, in which case dev mode can be off.
2. **Reverse proxy with TLS** (Caddy, nginx, Traefik) on a domain you own —
   see the reverse proxy section below. Once you're on HTTPS: turn
   `TAPETUM_DEV` off (Secure cookies work again) and set
   `TAPETUM_PUBLIC_URL=https://nvr.example.com`.
3. **Plain port-forward** — works but is not recommended: credentials and
   video cross the internet unencrypted. If you do it anyway,
   `TAPETUM_DEV=true` must stay on.

## Production deployment (single node, self-hosted)

Everything below is a `.env` edit — `docker-compose.yml` stays stock:

1. **Change the database password** — set `POSTGRES_PASSWORD` in `.env`. The
   app's `TAPETUM_DATABASE_URL` is constructed from it automatically.
2. **Postgres is not exposed.** The compose file publishes it on
   `127.0.0.1:5432` only (for local tooling) — nothing to do. To remove even
   that, drop the `ports:` block from the `db` service.
3. **Set the public URL** if you access Tapetum through a domain or reverse
   proxy — it's used in notification/export links:

   ```sh
   # .env
   TAPETUM_PUBLIC_URL=https://nvr.example.com
   ```

4. **Set a fixed secret key** (optional but recommended). Tapetum encrypts
   camera passwords and S3 credentials at rest with a 32-byte key. If unset,
   one is generated at `/data/server.key` (mode 0600) — which is fine as long
   as the `tapetum-data` volume survives. To manage it yourself:

   ```sh
   openssl rand -hex 32   # → TAPETUM_SECRET_KEY in .env
   ```

   Keep this key safe: losing it makes stored secrets undecryptable.
5. **Size the recording volume.** Continuous recording of one 4 Mbps camera
   is roughly **40 GB/day**. Set per-camera retention limits (days and/or GB)
   in the camera settings UI and the janitor enforces them automatically.

### Reverse proxy

Terminate TLS in front of Tapetum and proxy to `:8080`. WebSocket support is
required (live status/event feed). Example nginx block:

```nginx
server {
    listen 443 ssl;
    server_name nvr.example.com;
    # ssl_certificate / ssl_certificate_key ...

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;   # WebSocket
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 3600s;                 # long-lived HLS/WS
        client_max_body_size 0;
    }
}
```

**WebRTC caveat:** live WebRTC currently uses host ICE candidates only (no
STUN/TURN yet). It works great for browsers on the same LAN/VPN as the
server. From a Docker container behind NAT it may fail for remote viewers —
the UI falls back to MJPEG/HLS automatically. If you need WebRTC through
Docker NAT, run the app service with `network_mode: host`.

### Upgrades

```sh
git pull
docker compose up --build -d
```

Database migrations (goose, embedded in the binary) run automatically at
startup, so no manual migration step is needed. As with any NVR, back up
`pgdata` before major upgrades.

### Backup

Back up both volumes (or their host paths):

- `pgdata` — the entire metadata/index. Without it, recordings on disk are
  orphaned.
- `tapetum-data/server.key` — required to decrypt stored camera/S3 secrets.
  (Not needed if you set `TAPETUM_SECRET_KEY` and have it recorded.)

Recording footage itself is disposable/re-derivable — back it up only if you
need to preserve archive video.

## Bare-metal install (no Docker)

Prerequisites: **Go 1.26+**, **Node 20+** (frontend build only),
**ffmpeg** on PATH, and a running **Postgres 16**.

```sh
cp config.example.yaml config.yaml   # edit database.url at minimum
make build                           # builds web/dist + ./tapetumd
./tapetumd -config config.yaml
```

Example systemd unit:

```ini
[Unit]
Description=Tapetum NVR
After=network-online.target postgresql.service

[Service]
User=tapetum
WorkingDirectory=/opt/tapetum
ExecStart=/opt/tapetum/tapetumd -config /opt/tapetum/config.yaml
Restart=on-failure
Environment=TAPETUM_DATA_DIR=/var/lib/tapetum

[Install]
WantedBy=multi-user.target
```

## Configuration

Config resolves as: **defaults → YAML file → environment variables** (env
wins). In Docker the entrypoint reads `/data/config.yaml`; the file is
optional if env vars cover everything.

| YAML key | Env var | Default | Notes |
|---|---|---|---|
| `server.addr` | `TAPETUM_ADDR` | `:8080` | HTTP listen address |
| `server.public_url` | `TAPETUM_PUBLIC_URL` | — | External URL, used in notification links |
| `server.data_dir` | `TAPETUM_DATA_DIR` | `./data` (`/data` in Docker) | Recordings, transcode cache, `server.key` |
| `server.dev` | `TAPETUM_DEV` | `false` | "Plain-HTTP mode": drops the Secure cookie flag. **Required** for any non-TLS access (LAN IP included); turn off when serving over HTTPS |
| `server.pprof_addr` | `TAPETUM_PPROF_ADDR` | — | Go pprof listen address (e.g. `:6060`); empty = off. **No auth** — keep it localhost-bound (compose: `PPROF_BIND`) |
| `database.url` | `TAPETUM_DATABASE_URL` | `postgres://tapetum:tapetum@localhost:5432/tapetum?sslmode=disable` | Required Postgres |
| `log.level` | `TAPETUM_LOG_LEVEL` | `info` | `debug` \| `info` \| `warn` \| `error` |
| — | `TAPETUM_SECRET_KEY` | auto-generated | 64 hex chars; encrypts secrets at rest |

**S3 cold tier** is configured from the admin UI (Admin → Storage), not
the YAML file — credentials are stored encrypted in Postgres. Segments older
than the tiering threshold are moved to S3 but remain playable (playback
redirects to presigned URLs).

## Development

```sh
docker compose up -d db   # Postgres only
make dev                  # backend (:8080) + vite dev server with proxy
make test                 # go test ./...
make test-race            # race detector (needs Postgres + ffmpeg)
```

Layout:

```
cmd/tapetumd    daemon entrypoint
internal/       api, auth, camera, db, ingest, record, storage, live,
                webrtc, onvif, detect, events, notify, export, ...
web/            Vue 3 + TypeScript + Vite SPA (embedded into the binary)
docs/           design docs — the contract the code follows
```

## Documentation

| Doc | Contents |
|---|---|
| [`docs/00-overview.md`](docs/00-overview.md) | Vision, goals, tech choices |
| [`docs/01-architecture.md`](docs/01-architecture.md) | Components, data flow, deployment shapes |
| [`docs/02-data-model.md`](docs/02-data-model.md) | Postgres + ClickHouse schemas |
| [`docs/03-api.md`](docs/03-api.md) | REST / WebSocket / WebRTC API surface |
| [`docs/04-auth-rbac.md`](docs/04-auth-rbac.md) | Users, roles, sessions, tokens |
| [`docs/05-ingest-streaming.md`](docs/05-ingest-streaming.md) | RTSP ingest, recording, live, playback |
| [`docs/06-storage.md`](docs/06-storage.md) | Segment layout, S3 tiering, retention |
| [`docs/07-detection-notifications.md`](docs/07-detection-notifications.md) | Motion/AI events, notification channels |
| [`docs/08-frontend.md`](docs/08-frontend.md) | Vue app structure and pages |
| [`docs/09-roadmap.md`](docs/09-roadmap.md) | Phased delivery plan and status |
