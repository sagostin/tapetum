# Tapetum NVR — 04: Auth & RBAC

## Roles

Four built-in roles, ordered by privilege:

| Permission | admin | operator | viewer | live_only |
|---|:-:|:-:|:-:|:-:|
| Live view | ✅ | ✅ | ✅ | ✅ |
| WebRTC/MJPEG streams | ✅ | ✅ | ✅ | ✅ |
| PTZ control | ✅ | ✅ | ❌ | ❌ |
| Playback / timeline | ✅ | ✅ | ✅ | ❌ |
| Event feed + ack | ✅ | ✅ | ✅ | ❌ |
| Clip export / download | ✅ | ✅ | ❌ | ❌ |
| Snapshot access | ✅ | ✅ | ✅ | ❌ |
| Camera create/edit/delete | ✅ | ❌ | ❌ | ❌ |
| Camera discovery/probe | ✅ | ❌ | ❌ | ❌ |
| Notification channels/rules | ✅ | ❌ | ❌ | ❌ |
| Storage/settings config | ✅ | ❌ | ❌ | ❌ |
| User management | ✅ | ❌ | ❌ | ❌ |
| Audit log view | ✅ | ❌ | ❌ | ❌ |

Roles are **fixed** (no custom role builder) — simplicity beats flexibility
here. Fine-grained control happens via per-camera ACLs instead.

## Per-Camera ACLs

- `user_camera_access` maps a user to the cameras they may see.
- **Empty ACL = all cameras** (default; simple installs never touch this).
- Non-empty ACL = allowlist, intersected with role permissions.
- Admin bypasses ACLs. ACLs apply to: camera lists, live, playback, events,
  exports.
- Enforced in the API middleware, not in handlers: a `camAccess(user, cameraID)`
  helper guards every camera-scoped route; list endpoints auto-filter.

### Permission check pseudocode

```go
func authorize(u *User, perm Permission, camID *uuid.UUID) error {
    if u.Disabled { return ErrDisabled }
    if !rolePerms[u.Role].Has(perm) { return ErrForbidden }
    if camID != nil && u.Role != RoleAdmin {
        if ok, err := aclAllows(u.ID, *camID); err != nil || !ok {
            return ErrForbidden
        }
    }
    return nil
}
```

## Sessions

- Cookie: `tapetum_session`, `HttpOnly; Secure; SameSite=Lax; Path=/`.
- Server-side session rows (`sessions` table), token stored as SHA-256 hash.
- TTL: 30 days sliding (renewed on activity), absolute max 90 days.
- CSRF: double-submit cookie (`tapetum_csrf` + `X-CSRF-Token` header) for all
  cookie-authenticated mutations. Bearer-token requests are exempt (not
  cookie-based → not CSRF-able).
- Logout destroys the row; "sign out everywhere" deletes all user sessions.

## API Tokens

- `tap_<random-32-bytes-base62>`; only SHA-256 hash stored.
- Scoped: `live:read`, `playback:read`, `events:read`, `events:write`,
  `cameras:read`, `cameras:write`, `admin`. Scope must be within user's role.
- Optional expiry; `last_used_at` tracked; revocable individually.
- Intended for: Home Assistant, scripts, mobile widgets, third-party
  dashboards.

## Passwords & Secrets

- Passwords: argon2id (OWASP params: m=64MB, t=3, p=4), min length 10 for
  admins, 8 otherwise.
- Camera passwords + channel secrets: AES-256-GCM with a server key generated
  at first run (`data/server.key`, mode 0600) or supplied via env
  `TAPETUM_SECRET_KEY`. Never returned by the API (`password` is write-only).

## First-Run Wizard

1. `GET /api/v1/setup/status` → `{needs_setup: true}` when `users` is empty.
2. SPA routes to `/setup`: create admin account, instance name, timezone,
   optional: first camera, S3 config, SMTP.
3. `POST /api/v1/setup` creates the admin + writes settings, then logs in.
   Locked forever after (`needs_setup: false`).

## Audit Log

Every privileged mutation writes an `audit_log` row: user, action, target,
diff-ish detail, IP, timestamp. Covers: user CRUD/role changes, camera
CRUD, settings changes, notification changes, export creation, segment
protect/unprotect. Viewable in admin UI, retention 1 year.

## Later (phase 5)

- **OIDC** (Authelia/Authentik/Keycloak): `oidc_subject` column already in
  schema; role mapping from claims; local login disableable.
- **TOTP 2FA**: `totp_secret` column present; enforced per-role via settings.
- **IP allowlists** for admin routes.

## Threat Notes

- All ingest URLs validated server-side (SSRF guard on `/cameras/probe` and
  ONVIF endpoints: no link-local/loopback unless explicitly allowed — it IS a
  LAN camera tool, but block cloud-metadata ranges 169.254.169.254 etc.).
- Snapshot/segment routes never expose raw filesystem paths — IDs resolved
  through the index only.
- Rate limits per `03-api.md`; bcrypt/argon cost keeps login brute-force slow.
- Security headers: CSP (self + blob: for MSE), X-Frame-Options DENY,
  nosniff, Referrer-Policy no-referrer.
