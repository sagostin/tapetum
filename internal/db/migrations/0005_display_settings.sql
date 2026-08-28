-- +goose Up
-- Phase 4 (UI3 wall): per-camera display orientation persisted server-side.
-- These power the dashboard wall tiles' rotate / H-flip / V-flip controls.

ALTER TABLE cameras ADD COLUMN display_rotate SMALLINT NOT NULL DEFAULT 0
    CHECK (display_rotate IN (0, 90, 180, 270));
ALTER TABLE cameras ADD COLUMN display_hflip BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE cameras ADD COLUMN display_vflip BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE cameras DROP COLUMN display_vflip;
ALTER TABLE cameras DROP COLUMN display_hflip;
ALTER TABLE cameras DROP COLUMN display_rotate;
