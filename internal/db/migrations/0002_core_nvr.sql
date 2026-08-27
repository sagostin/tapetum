-- Phase 1: cameras, recording index, exports. See docs/02-data-model.md.

-- +goose Up
CREATE TABLE camera_groups (
    id    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name  text NOT NULL UNIQUE
);

CREATE TABLE cameras (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name            text NOT NULL,
    enabled         boolean NOT NULL DEFAULT true,

    main_url        text NOT NULL,
    sub_url         text,
    username        text NOT NULL DEFAULT '',
    password_enc    bytea,
    transport       text NOT NULL DEFAULT 'tcp' CHECK (transport IN ('tcp','udp','auto')),

    onvif_endpoint  text,
    onvif_profile   text,
    has_ptz         boolean NOT NULL DEFAULT false,

    record_mode     text NOT NULL DEFAULT 'continuous'
                    CHECK (record_mode IN ('continuous','motion','off')),
    retention_days  int NOT NULL DEFAULT 14,
    retention_gb    int,
    tier_after_days int,

    motion_config   jsonb NOT NULL DEFAULT '{}',
    ai_config       jsonb NOT NULL DEFAULT '{}',

    group_id        uuid REFERENCES camera_groups(id) ON DELETE SET NULL,

    status          text NOT NULL DEFAULT 'offline'
                    CHECK (status IN ('online','offline','degraded')),
    status_detail   jsonb NOT NULL DEFAULT '{}',
    last_seen_at    timestamptz,

    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE user_camera_access (
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    camera_id  uuid NOT NULL REFERENCES cameras(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, camera_id)
);

CREATE TABLE recording_segments (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    camera_id    uuid NOT NULL REFERENCES cameras(id) ON DELETE CASCADE,
    start_ts     timestamptz NOT NULL,
    end_ts       timestamptz NOT NULL,
    duration_ms  int GENERATED ALWAYS AS
                 (extract(epoch from (end_ts - start_ts)) * 1000) STORED,
    storage      text NOT NULL DEFAULT 'local' CHECK (storage IN ('local','s3')),
    path         text NOT NULL,
    size_bytes   bigint NOT NULL,
    has_motion   boolean NOT NULL DEFAULT false,
    protected    boolean NOT NULL DEFAULT false,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (camera_id, path)
);
CREATE INDEX idx_segments_cam_start ON recording_segments (camera_id, start_ts);
CREATE INDEX idx_segments_cam_motion ON recording_segments (camera_id, start_ts) WHERE has_motion;

CREATE TABLE recording_gaps (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    camera_id  uuid NOT NULL REFERENCES cameras(id) ON DELETE CASCADE,
    start_ts   timestamptz NOT NULL,
    end_ts     timestamptz,
    reason     text NOT NULL DEFAULT ''
);
CREATE INDEX idx_gaps_cam_start ON recording_gaps (camera_id, start_ts);

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
CREATE INDEX idx_exports_user ON exports (user_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS exports;
DROP TABLE IF EXISTS recording_gaps;
DROP TABLE IF EXISTS recording_segments;
DROP TABLE IF EXISTS user_camera_access;
DROP TABLE IF EXISTS cameras;
DROP TABLE IF EXISTS camera_groups;
