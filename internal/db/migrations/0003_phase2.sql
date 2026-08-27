-- Phase 2: ONVIF/PTZ imaging config, playback transcode policy.
-- S3 config lives in the settings table (no schema needed).

-- +goose Up
ALTER TABLE cameras ADD COLUMN imaging_config jsonb NOT NULL DEFAULT '{}';
ALTER TABLE cameras ADD COLUMN playback_transcode text NOT NULL DEFAULT 'auto'
    CHECK (playback_transcode IN ('auto','never','always'));

-- +goose Down
ALTER TABLE cameras DROP COLUMN playback_transcode;
ALTER TABLE cameras DROP COLUMN imaging_config;
