# Tapetum NVR — 02: Data Model

Postgres is the **system of record** and is always required. ClickHouse is an
**optional analytical tier** — enabled via config, never required. All features
work without it; when present, high-volume detection/telemetry data is written
there instead of (or in addition to) Postgres.

## Postgres Schema

Migrations via `goose`. All timestamps `timestamptz`. IDs are `uuid`
(`gen_random_uuid()`).

### users / auth

```sql
CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    username      citext NOT NULL UNIQUE,
    display_name  text NOT NULL DEFAULT '',
    email         citext UNIQUE,
    password_hash text NOT NULL,               -- argon2id
    role          text NOT NULL DEFAULT 'viewer'
                  CHECK (role IN ('admin','operator','viewer','live_only')),
    disabled      boolean NOT NULL DEFAULT false,
    totp_secret   bytea,                       -- phase 5: 2FA
    oidc_subject  text UNIQUE,                 -- phase 5: OIDC link
    last_login_at timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  bytea NOT NULL UNIQUE,         -- sha256 of cookie value
    ip          inet,
    user_agent  text,
    expires_at  timestamptz NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON sessions (user_id);

CREATE TABLE api_tokens (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name        text NOT NULL,
    token_hash  bytea NOT NULL UNIQUE,
    scopes      text[] NOT NULL DEFAULT '{}',  -- e.g. {events:read, playback:read}
    expires_at  timestamptz,
    last_used_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);
```

### camera ACLs

```sql
CREATE TABLE camera_groups (
    id    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name  text NOT NULL UNIQUE
);

-- When empty for a user → user can access all cameras allowed by their role.
CREATE TABLE user_camera_access (
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    camera_id  uuid NOT NULL REFERENCES cameras(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, camera_id)
);
```

### cameras

```sql
CREATE TABLE cameras (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name            text NOT NULL,
    enabled         boolean NOT NULL DEFAULT true,

    -- connection
    main_url        text NOT NULL,             -- rtsp://… (main stream)
    sub_url         text,                      -- rtsp://… (sub stream)
    username        text NOT NULL DEFAULT '',
    password_enc    bytea,                     -- encrypted at rest (age or AES-GCM w/ server key)
    transport       text NOT NULL DEFAULT 'tcp' CHECK (transport IN ('tcp','udp','auto')),

    -- ONVIF
    onvif_endpoint  text,                      -- http://ip/onvif/device_service
    onvif_profile   text,                      -- selected media profile token
    has_ptz         boolean NOT NULL DEFAULT false,
    imaging_config  jsonb NOT NULL DEFAULT '{}', -- cached ONVIF video source token etc.

    -- recording policy
    record_mode     text NOT NULL DEFAULT 'continuous'
                    CHECK (record_mode IN ('continuous','motion','off')),
    retention_days  int NOT NULL DEFAULT 14,
    retention_gb    int,                       -- optional cap; oldest evicted first
    tier_after_days int,                       -- move to S3 after N days (null = never)
    playback_transcode text NOT NULL DEFAULT 'auto'  -- H.265→H.264 fallback policy
                    CHECK (playback_transcode IN ('auto','never','always')),

    -- detection config (phase 3+)
    motion_config   jsonb NOT NULL DEFAULT '{}',  -- zones, sensitivity, schedule
    ai_config       jsonb NOT NULL DEFAULT '{}',  -- labels, confidence, schedule

    group_id        uuid REFERENCES camera_groups(id) ON DELETE SET NULL,

    -- live status (denormalized, updated by ingest worker)
    status          text NOT NULL DEFAULT 'offline'
                    CHECK (status IN ('online','offline','degraded')),
    status_detail   jsonb NOT NULL DEFAULT '{}', -- bitrate, fps, last error
    last_seen_at    timestamptz,

    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
```

### recording index

```sql
CREATE TABLE recording_segments (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    camera_id    uuid NOT NULL REFERENCES cameras(id) ON DELETE CASCADE,
    start_ts     timestamptz NOT NULL,         -- from RTP NTP mapping
    end_ts       timestamptz NOT NULL,
    duration_ms  int GENERATED ALWAYS AS
                 (extract(epoch from (end_ts - start_ts)) * 1000) STORED,
    storage      text NOT NULL DEFAULT 'local' CHECK (storage IN ('local','s3')),
    path         text NOT NULL,                -- relative key: cam/2026/08/27/13/…m4s
    size_bytes   bigint NOT NULL,
    has_motion   boolean NOT NULL DEFAULT false, -- denormalized for timeline density
    protected    boolean NOT NULL DEFAULT false, -- pinned by an event; janitor skips
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (camera_id, path)
);
CREATE INDEX ON recording_segments (camera_id, start_ts);
CREATE INDEX ON recording_segments (camera_id, start_ts) WHERE has_motion;

CREATE TABLE recording_gaps (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    camera_id  uuid NOT NULL REFERENCES cameras(id) ON DELETE CASCADE,
    start_ts   timestamptz NOT NULL,
    end_ts     timestamptz,                    -- null while gap is open
    reason     text NOT NULL DEFAULT ''        -- 'camera offline', 'ingest restart', …
);
CREATE INDEX ON recording_gaps (camera_id, start_ts);
```

### events

Default store. Monthly range partitioning keeps this fast at home-install
volumes; high-volume installs redirect detections to ClickHouse.

```sql
CREATE TABLE events (
    id          uuid NOT NULL DEFAULT gen_random_uuid(),
    camera_id   uuid NOT NULL REFERENCES cameras(id) ON DELETE CASCADE,
    ts          timestamptz NOT NULL,
    end_ts      timestamptz,
    type        text NOT NULL                  -- 'motion','ai','camera.offline',…
                CHECK (type IN ('motion','ai','camera_offline','camera_online',
                                'storage_warning','system')),
    -- AI fields (null for motion)
    label       text,                          -- 'person','car','dog',…
    confidence  real,
    bbox        jsonb,                         -- {x,y,w,h} normalized
    -- artifacts
    snapshot_path text,                        -- storage key of JPEG
    clip_start_ts timestamptz,                 -- playback range incl. pre/post roll
    clip_end_ts   timestamptz,
    -- bookkeeping
    notified_at timestamptz,
    acked_by    uuid REFERENCES users(id),
    acked_at    timestamptz,
    metadata    jsonb NOT NULL DEFAULT '{}',
    PRIMARY KEY (ts, id)
) PARTITION BY RANGE (ts);

-- one partition per month, created by a maintenance task
CREATE INDEX ON events (camera_id, ts);
CREATE INDEX ON events (type, ts);
```

### notifications

```sql
CREATE TABLE notification_channels (
    id      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name    text NOT NULL,
    type    text NOT NULL CHECK (type IN
            ('smtp','webhook','ntfy','gotify','discord','slack','telegram')),
    config  jsonb NOT NULL,                    -- secrets encrypted
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE notification_rules (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text NOT NULL,
    enabled     boolean NOT NULL DEFAULT true,
    camera_ids  uuid[] NOT NULL DEFAULT '{}',  -- empty = all
    event_types text[] NOT NULL DEFAULT '{motion}',
    labels      text[] NOT NULL DEFAULT '{}',  -- AI label filter; empty = any
    schedule    jsonb NOT NULL DEFAULT '{}',   -- cron-ish windows
    cooldown_s  int NOT NULL DEFAULT 300,
    channel_ids uuid[] NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE notification_log (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    rule_id    uuid REFERENCES notification_rules(id) ON DELETE SET NULL,
    channel_id uuid REFERENCES notification_channels(id) ON DELETE SET NULL,
    event_ts   timestamptz,
    event_id   uuid,
    status     text NOT NULL,                  -- 'sent','failed','cooldown_skip'
    error      text,
    sent_at    timestamptz NOT NULL DEFAULT now()
);
```

### system

```sql
CREATE TABLE audit_log (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    uuid REFERENCES users(id) ON DELETE SET NULL,
    action     text NOT NULL,                  -- 'camera.create','user.role_change',…
    target     text,                           -- resource id
    detail     jsonb NOT NULL DEFAULT '{}',
    ip         inet,
    ts         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON audit_log (ts);

CREATE TABLE settings (                        -- singleton-ish key/value for system config
    key   text PRIMARY KEY,                    -- 's3' (secret_key_enc AES-GCM at rest), …
    value jsonb NOT NULL
);

CREATE TABLE exports (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    camera_id  uuid NOT NULL REFERENCES cameras(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    start_ts   timestamptz NOT NULL,
    end_ts     timestamptz NOT NULL,
    status     text NOT NULL DEFAULT 'pending'
               CHECK (status IN ('pending','processing','done','failed')),
    path       text,
    size_bytes bigint,
    error      text,
    created_at timestamptz NOT NULL DEFAULT now()
);
```

## ClickHouse Schema (optional module)

Enabled with `analytics.clickhouse.url` in config. Purpose: **high-volume,
append-only analytical data** — AI detections at scale, per-frame motion
telemetry, long-term heatmaps and dashboards. Postgres remains authoritative
for anything relational/transactional (users, cameras, segment index, alert
rules).

Written by the `events.Store` ClickHouse implementation. ClickHouse does not
replace the `events` table for UI-critical workflows unless explicitly
configured (`analytics.detection_store: clickhouse`).

```sql
-- One row per detection (motion tick or AI inference)
CREATE TABLE detections (
    ts          DateTime64(3),
    camera_id   UUID,
    type        LowCardinality(String),        -- 'motion' | 'ai'
    label       LowCardinality(String),        -- '' for motion
    confidence  Float32,
    bbox        Tuple(x Float32, y Float32, w Float32, h Float32),
    zone        LowCardinality(String),
    event_id    UUID                           -- links back to Postgres events.id
) ENGINE = MergeTree
PARTITION BY toYYYYMM(ts)
ORDER BY (camera_id, ts)
TTL ts + INTERVAL 12 MONTH;

-- Aggregated per-minute motion intensity (feeds timeline heatmaps cheaply)
CREATE MATERIALIZED VIEW motion_minutes
ENGINE = SummingMergeTree
PARTITION BY toYYYYMM(minute)
ORDER BY (camera_id, minute)
AS SELECT
    toStartOfMinute(ts) AS minute,
    camera_id,
    count()          AS ticks,
    avg(confidence)  AS avg_intensity
FROM detections WHERE type = 'motion'
GROUP BY camera_id, minute;
```

### What goes where

| Data | Postgres | ClickHouse |
|---|---|---|
| Users, sessions, tokens, ACLs | ✅ | — |
| Cameras, settings, audit log | ✅ | — |
| Segment index, gaps, exports | ✅ | — |
| UI event feed (alerts) | ✅ (default) | optional redirect |
| Raw detection ticks / AI frames | — | ✅ |
| Motion heatmap aggregation | fallback (slow) | ✅ fast |
| Analytics dashboards (events/day/camera/label) | fallback | ✅ |

### Interface seam

```go
type EventStore interface {
    WriteDetection(ctx context.Context, d Detection) error
    QueryEvents(ctx context.Context, f EventFilter) ([]Event, error)
    MotionDensity(ctx context.Context, cam uuid.UUID, from, to time.Time, buckets int) ([]float64, error)
}
```

- `postgresEventStore` — default; `MotionDensity` computed via
  `generate_series` bucketing over `events`.
- `clickhouseEventStore` — uses `motion_minutes` MV for density; near-instant
  over months of data.
- Selected by config; the API layer never knows which is active.

## Retention & Consistency Rules

1. A segment file and its `recording_segments` row are created/deleted
   **together** (row first on create, object first on delete, reconcile job
   cleans orphans).
2. `protected = true` segments are never evicted; set when an event references
   them.
3. S3 tiering updates `storage`/`path` atomically; playback resolves location
   per segment at request time.
4. Event `clip_start_ts`/`clip_end_ts` are authoritative for playback; segments
   are derived from the index at request time.
