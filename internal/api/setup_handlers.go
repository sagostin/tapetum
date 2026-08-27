package api

import (
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/sagostin/tapetum/internal/audit"
	"github.com/sagostin/tapetum/internal/auth"
)

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9._-]{3,32}$`)

func validateUsername(s string) bool { return usernameRe.MatchString(s) }

func validatePassword(s string, minLen int) bool {
	return utf8.RuneCountInString(s) >= minLen
}

// --- setup ---------------------------------------------------------------

func (s *Server) needsSetup(r *http.Request) bool {
	var n int
	if err := s.pool.QueryRow(r.Context(), `SELECT count(*) FROM users`).Scan(&n); err != nil {
		return false
	}
	return n == 0
}

func (s *Server) setupStatus(w http.ResponseWriter, r *http.Request) {
	JSON(w, http.StatusOK, map[string]bool{"needs_setup": s.needsSetup(r)})
}

// requireSetupPending blocks POST /setup once any user exists.
func (s *Server) requireSetupPending(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.needsSetup(r) {
			Error(w, http.StatusForbidden, "setup_complete", "setup already completed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type setupRequest struct {
	Username     string `json:"username"`
	Password     string `json:"password"`
	DisplayName  string `json:"display_name"`
	InstanceName string `json:"instance_name"`
}

func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	var req setupRequest
	if err := Decode(w, r, &req); err != nil {
		Error(w, http.StatusBadRequest, "bad_request", "invalid request body")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if !validateUsername(req.Username) {
		Error(w, http.StatusBadRequest, "invalid_username",
			"username must be 3–32 chars: letters, digits, . _ -")
		return
	}
	if !validatePassword(req.Password, 10) {
		Error(w, http.StatusBadRequest, "weak_password", "admin password must be at least 10 characters")
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", "could not hash password")
		return
	}

	var userID string
	err = s.pool.QueryRow(r.Context(),
		`INSERT INTO users (username, display_name, password_hash, role)
		 VALUES ($1, $2, $3, 'admin') RETURNING id`,
		req.Username, req.DisplayName, hash).Scan(&userID)
	if err != nil {
		Error(w, http.StatusConflict, "conflict", "could not create admin user (already exists?)")
		return
	}

	if req.InstanceName != "" {
		_, _ = s.pool.Exec(r.Context(),
			`INSERT INTO settings (key, value) VALUES ('instance_name', to_jsonb($1::text))
			 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, req.InstanceName)
	}

	audit.Log(r.Context(), s.pool, audit.Entry{
		UserID: userID, Action: "setup.complete", IP: clientIP(r),
	})

	// Auto-login after setup.
	token, err := s.auth.CreateSession(r.Context(), userID, clientIP(r), r.UserAgent())
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", "could not create session")
		return
	}
	if err := s.auth.SetSessionCookies(w, token); err != nil {
		Error(w, http.StatusInternalServerError, "internal", "could not set cookies")
		return
	}

	JSON(w, http.StatusCreated, map[string]any{"id": userID, "username": req.Username, "role": "admin"})
}
