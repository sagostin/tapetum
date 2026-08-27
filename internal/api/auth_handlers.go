package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/sagostin/tapetum/internal/audit"
	"github.com/sagostin/tapetum/internal/auth"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.limiter.Allow(ip) {
		Error(w, http.StatusTooManyRequests, "rate_limited",
			"too many login attempts; try again in a few minutes")
		return
	}

	var req loginRequest
	if err := Decode(w, r, &req); err != nil {
		Error(w, http.StatusBadRequest, "bad_request", "invalid request body")
		return
	}

	row := s.pool.QueryRow(r.Context(),
		`SELECT id, username, display_name, email, role, disabled,
		        password_hash, last_login_at, created_at
		 FROM users WHERE username = $1`, strings.TrimSpace(req.Username))
	var u auth.User
	var hash string
	err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.Role,
		&u.Disabled, &hash, &u.LastLoginAt, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		s.limiter.Fail(ip)
		Error(w, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
		return
	}
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", "login failed")
		return
	}

	ok, err := auth.VerifyPassword(req.Password, hash)
	if err != nil || !ok {
		s.limiter.Fail(ip)
		audit.Log(r.Context(), s.pool, audit.Entry{
			UserID: u.ID, Action: "auth.login_failed", IP: ip,
		})
		Error(w, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
		return
	}
	if u.Disabled {
		Error(w, http.StatusForbidden, "account_disabled", "this account is disabled")
		return
	}

	s.limiter.Succeed(ip)
	token, err := s.auth.CreateSession(r.Context(), u.ID, ip, r.UserAgent())
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", "could not create session")
		return
	}
	if err := s.auth.SetSessionCookies(w, token); err != nil {
		Error(w, http.StatusInternalServerError, "internal", "could not set cookies")
		return
	}
	_, _ = s.pool.Exec(r.Context(), `UPDATE users SET last_login_at = now() WHERE id = $1`, u.ID)
	audit.Log(r.Context(), s.pool, audit.Entry{UserID: u.ID, Action: "auth.login", IP: ip})

	JSON(w, http.StatusOK, u.Public())
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.SessionCookie); err == nil {
		_ = s.auth.DestroySession(r.Context(), c.Value)
	}
	s.auth.ClearSessionCookies(w)
	JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	JSON(w, http.StatusOK, auth.UserFrom(r.Context()).Public())
}

type passwordRequest struct {
	Current string `json:"current"`
	New     string `json:"new"`
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	var req passwordRequest
	if err := Decode(w, r, &req); err != nil {
		Error(w, http.StatusBadRequest, "bad_request", "invalid request body")
		return
	}
	minLen := 8
	if u.Role == auth.RoleAdmin {
		minLen = 10
	}
	if !validatePassword(req.New, minLen) {
		Error(w, http.StatusBadRequest, "weak_password",
			"password must be at least "+string(rune('0'+minLen))+" characters")
		return
	}

	var hash string
	if err := s.pool.QueryRow(r.Context(),
		`SELECT password_hash FROM users WHERE id = $1`, u.ID).Scan(&hash); err != nil {
		Error(w, http.StatusInternalServerError, "internal", "lookup failed")
		return
	}
	ok, err := auth.VerifyPassword(req.Current, hash)
	if err != nil || !ok {
		Error(w, http.StatusUnauthorized, "invalid_credentials", "current password is incorrect")
		return
	}

	newHash, err := auth.HashPassword(req.New)
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", "could not hash password")
		return
	}
	if _, err := s.pool.Exec(r.Context(),
		`UPDATE users SET password_hash = $1, updated_at = now() WHERE id = $2`, newHash, u.ID); err != nil {
		Error(w, http.StatusInternalServerError, "internal", "update failed")
		return
	}

	// Keep current session, kill the rest.
	if c, err := r.Cookie(auth.SessionCookie); err == nil {
		_ = s.auth.DestroyOtherSessions(r.Context(), u.ID, c.Value)
	}
	audit.Log(r.Context(), s.pool, audit.Entry{UserID: u.ID, Action: "auth.password_change", IP: clientIP(r)})
	JSON(w, http.StatusOK, map[string]bool{"ok": true})
}
