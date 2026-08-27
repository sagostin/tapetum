-- +goose Up
-- Phase 0: auth core (users, sessions, tokens), audit log, settings.
-- Cameras/segments/events arrive in later-phase migrations.

CREATE EXTENSION IF NOT EXISTS citext;

CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    username      citext NOT NULL UNIQUE,
    display_name  text NOT NULL DEFAULT '',
    email         citext UNIQUE,
    password_hash text NOT NULL,
    role          text NOT NULL DEFAULT 'viewer'
                  CHECK (role IN ('admin','operator','viewer','live_only')),
    disabled      boolean NOT NULL DEFAULT false,
    totp_secret   bytea,
    oidc_subject  text UNIQUE,
    last_login_at timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  bytea NOT NULL UNIQUE,
    ip          inet,
    user_agent  text,
    expires_at  timestamptz NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_user_id_idx ON sessions (user_id);
CREATE INDEX sessions_expires_idx ON sessions (expires_at);

CREATE TABLE api_tokens (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         text NOT NULL,
    token_hash   bytea NOT NULL UNIQUE,
    scopes       text[] NOT NULL DEFAULT '{}',
    expires_at   timestamptz,
    last_used_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX api_tokens_user_id_idx ON api_tokens (user_id);

CREATE TABLE audit_log (
    id      bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    action  text NOT NULL,
    target  text,
    detail  jsonb NOT NULL DEFAULT '{}',
    ip      inet,
    ts      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_log_ts_idx ON audit_log (ts);

CREATE TABLE settings (
    key   text PRIMARY KEY,
    value jsonb NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS settings;
DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS api_tokens;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS users;
DROP EXTENSION IF EXISTS citext;
