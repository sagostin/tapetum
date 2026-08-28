-- +goose Up
-- Phase 3: motion events & notifications (docs/07-detection-notifications.md)

-- Events, partitioned by month on ts. The events store maintains partitions
-- (current + next month) at boot and daily; a default partition catches
-- anything outside the maintained range.
CREATE TABLE events (
    id          uuid NOT NULL DEFAULT gen_random_uuid(),
    camera_id   uuid NOT NULL REFERENCES cameras(id) ON DELETE CASCADE,
    ts          timestamptz NOT NULL,
    end_ts      timestamptz,
    type        text NOT NULL
                CHECK (type IN ('motion','ai','camera_offline','camera_online',
                                'storage_warning','system')),
    label       text,
    confidence  real,
    bbox        jsonb,
    snapshot_path text,
    clip_start_ts timestamptz,
    clip_end_ts   timestamptz,
    notified_at timestamptz,
    acked_by    uuid REFERENCES users(id),
    acked_at    timestamptz,
    metadata    jsonb NOT NULL DEFAULT '{}',
    PRIMARY KEY (ts, id)
) PARTITION BY RANGE (ts);

CREATE TABLE events_default PARTITION OF events DEFAULT;
CREATE INDEX ON events (camera_id, ts);
CREATE INDEX ON events (type, ts);
CREATE INDEX ON events (id);

-- Notification channels: config holds per-type fields; secrets are stored
-- AES-GCM-encrypted under "*_enc" keys (same scheme as the S3 secret).
CREATE TABLE notification_channels (
    id      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name    text NOT NULL,
    type    text NOT NULL CHECK (type IN
            ('smtp','webhook','ntfy','gotify','discord','slack','telegram')),
    config  jsonb NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE notification_rules (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text NOT NULL,
    enabled     boolean NOT NULL DEFAULT true,
    camera_ids  uuid[] NOT NULL DEFAULT '{}',  -- empty = all
    event_types text[] NOT NULL DEFAULT '{motion}',
    labels      text[] NOT NULL DEFAULT '{}',
    schedule    jsonb NOT NULL DEFAULT '{}',   -- {"mon": [["21:00","07:00"]], …}
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
    status     text NOT NULL CHECK (status IN ('sent','failed','cooldown_skip')),
    error      text,
    sent_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON notification_log (sent_at);
