package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// SessionCookie carries the session token (HttpOnly).
	SessionCookie = "tapetum_session"
	// CSRFCookie carries the double-submit CSRF token (JS-readable).
	CSRFCookie = "tapetum_csrf"

	sessionTTL        = 30 * 24 * time.Hour // sliding
	sessionAbsolute   = 90 * 24 * time.Hour // hard cap from creation
	sessionRenewAfter = 24 * time.Hour      // renew if older than this
)

var ErrUnauthorized = errors.New("unauthorized")

type ctxKey struct{}

// Manager owns session and API-token authentication against Postgres.
type Manager struct {
	pool   *pgxpool.Pool
	secure bool // Secure cookie flag (off in dev/http)
}

func NewManager(pool *pgxpool.Pool, dev bool) *Manager {
	return &Manager{pool: pool, secure: !dev}
}

// --- sessions -------------------------------------------------------------

// CreateSession creates a session row and returns the cookie token.
func (m *Manager) CreateSession(ctx context.Context, userID, ip, userAgent string) (string, error) {
	token, hash, err := NewToken()
	if err != nil {
		return "", err
	}
	_, err = m.pool.Exec(ctx,
		`INSERT INTO sessions (user_id, token_hash, ip, user_agent, expires_at)
		 VALUES ($1, $2, $3::inet, $4, $5)`,
		userID, hash, nullIfEmpty(ip), userAgent, time.Now().Add(sessionTTL))
	if err != nil {
		return "", err
	}
	return token, nil
}

// DestroySession deletes the session matching the token.
func (m *Manager) DestroySession(ctx context.Context, token string) error {
	_, err := m.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, HashToken(token))
	return err
}

// DestroyOtherSessions deletes all of a user's sessions except the given one.
func (m *Manager) DestroyOtherSessions(ctx context.Context, userID, keepToken string) error {
	_, err := m.pool.Exec(ctx,
		`DELETE FROM sessions WHERE user_id = $1 AND token_hash <> $2`,
		userID, HashToken(keepToken))
	return err
}

func (m *Manager) lookupSession(ctx context.Context, token string) (*User, error) {
	row := m.pool.QueryRow(ctx,
		`SELECT u.id, u.username, u.display_name, u.email, u.role, u.disabled,
		        u.password_hash, u.last_login_at, u.created_at,
		        s.id, s.created_at, s.expires_at
		 FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.token_hash = $1`, HashToken(token))

	var u User
	var sessionID string
	var sessionCreated, sessionExpires time.Time
	err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.Role,
		&u.Disabled, &u.passwordHash, &u.LastLoginAt, &u.CreatedAt,
		&sessionID, &sessionCreated, &sessionExpires)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}

	now := time.Now()
	if u.Disabled || now.After(sessionExpires) || sessionCreated.Add(sessionAbsolute).Before(now) {
		return nil, ErrUnauthorized
	}
	// Sliding renewal.
	if now.Sub(sessionCreated) > 0 && sessionExpires.Sub(now) < sessionTTL-sessionRenewAfter {
		_, _ = m.pool.Exec(ctx,
			`UPDATE sessions SET expires_at = $1 WHERE id = $2`, now.Add(sessionTTL), sessionID)
	}
	return &u, nil
}

// --- API tokens -----------------------------------------------------------

func (m *Manager) lookupAPIToken(ctx context.Context, token string) (*User, error) {
	row := m.pool.QueryRow(ctx,
		`SELECT u.id, u.username, u.display_name, u.email, u.role, u.disabled,
		        u.password_hash, u.last_login_at, u.created_at, t.id, t.expires_at
		 FROM api_tokens t JOIN users u ON u.id = t.user_id
		 WHERE t.token_hash = $1`, HashToken(token))

	var u User
	var tokenID string
	var expires *time.Time
	err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.Role,
		&u.Disabled, &u.passwordHash, &u.LastLoginAt, &u.CreatedAt, &tokenID, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	if u.Disabled || (expires != nil && time.Now().After(*expires)) {
		return nil, ErrUnauthorized
	}
	_, _ = m.pool.Exec(ctx, `UPDATE api_tokens SET last_used_at = now() WHERE id = $1`, tokenID)
	u.ViaToken = true
	return &u, nil
}

// --- middleware -----------------------------------------------------------

// Middleware authenticates requests via session cookie or Bearer token and
// stores the *User in the request context. Unauthenticated requests continue
// with no user in context — route-level RequireAuth/Require decide.
func (m *Manager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var user *User

		if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
			if u, err := m.lookupSession(r.Context(), c.Value); err == nil {
				user = u
			}
		}
		if user == nil {
			if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
				if u, err := m.lookupAPIToken(r.Context(), strings.TrimPrefix(h, "Bearer ")); err == nil {
					user = u
				}
			}
		}

		if user != nil {
			r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, user))
		}
		next.ServeHTTP(w, r)
	})
}

// UserFrom returns the authenticated user, or nil.
func UserFrom(ctx context.Context) *User {
	u, _ := ctx.Value(ctxKey{}).(*User)
	return u
}

// --- cookies --------------------------------------------------------------

// SetSessionCookies writes the session cookie and a fresh CSRF cookie.
func (m *Manager) SetSessionCookies(w http.ResponseWriter, sessionToken string) error {
	csrf := make([]byte, 32)
	if _, err := rand.Read(csrf); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: sessionToken, Path: "/",
		HttpOnly: true, Secure: m.secure, SameSite: http.SameSiteLaxMode,
		MaxAge: int(sessionTTL.Seconds()),
	})
	http.SetCookie(w, &http.Cookie{
		Name: CSRFCookie, Value: base64.RawURLEncoding.EncodeToString(csrf), Path: "/",
		HttpOnly: false, Secure: m.secure, SameSite: http.SameSiteLaxMode,
		MaxAge: int(sessionTTL.Seconds()),
	})
	return nil
}

// ClearSessionCookies expires both cookies.
func (m *Manager) ClearSessionCookies(w http.ResponseWriter) {
	for _, name := range []string{SessionCookie, CSRFCookie} {
		http.SetCookie(w, &http.Cookie{
			Name: name, Value: "", Path: "/", MaxAge: -1,
			HttpOnly: name == SessionCookie, Secure: m.secure, SameSite: http.SameSiteLaxMode,
		})
	}
}

// CSRF middleware enforces the double-submit cookie pattern on mutating
// requests authenticated by cookie. Bearer-token requests are exempt.
func (m *Manager) CSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		u := UserFrom(r.Context())
		if u == nil || u.ViaToken {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(CSRFCookie)
		if err != nil || cookie.Value == "" || cookie.Value != r.Header.Get("X-CSRF-Token") {
			http.Error(w, `{"error":{"code":"csrf","message":"CSRF token missing or invalid"}}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
