package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/sagostin/tapetum/internal/audit"
	"github.com/sagostin/tapetum/internal/auth"
)

// --- users (admin) --------------------------------------------------------

type userRow struct {
	ID          string     `json:"id"`
	Username    string     `json:"username"`
	DisplayName string     `json:"display_name"`
	Email       *string    `json:"email"`
	Role        string     `json:"role"`
	Disabled    bool       `json:"disabled"`
	LastLoginAt *time.Time `json:"last_login_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

const userCols = `id, username, display_name, email, role, disabled, last_login_at, created_at`

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.pool.Query(r.Context(), `SELECT `+userCols+` FROM users ORDER BY username`)
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()

	users := []userRow{}
	for rows.Next() {
		var u userRow
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.Role,
			&u.Disabled, &u.LastLoginAt, &u.CreatedAt); err != nil {
			Error(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		users = append(users, u)
	}
	JSON(w, http.StatusOK, map[string]any{"users": users})
}

type createUserRequest struct {
	Username    string  `json:"username"`
	DisplayName string  `json:"display_name"`
	Email       *string `json:"email"`
	Password    string  `json:"password"`
	Role        string  `json:"role"`
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	actor := auth.UserFrom(r.Context())
	var req createUserRequest
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
	if !auth.ValidRole(req.Role) {
		Error(w, http.StatusBadRequest, "invalid_role", "unknown role")
		return
	}
	minLen := 8
	if auth.Role(req.Role) == auth.RoleAdmin {
		minLen = 10
	}
	if !validatePassword(req.Password, minLen) {
		Error(w, http.StatusBadRequest, "weak_password", "password too short for role")
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", "could not hash password")
		return
	}

	var u userRow
	err = s.pool.QueryRow(r.Context(),
		`INSERT INTO users (username, display_name, email, password_hash, role)
		 VALUES ($1, $2, NULLIF($3,''), $4, $5) RETURNING `+userCols,
		req.Username, req.DisplayName, strOrEmpty(req.Email), hash, req.Role).
		Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.Role,
			&u.Disabled, &u.LastLoginAt, &u.CreatedAt)
	if err != nil {
		Error(w, http.StatusConflict, "conflict", "username or email already in use")
		return
	}
	audit.Log(r.Context(), s.pool, audit.Entry{
		UserID: actor.ID, Action: "user.create", Target: u.ID,
		Detail: map[string]any{"username": u.Username, "role": u.Role}, IP: clientIP(r),
	})
	JSON(w, http.StatusCreated, u)
}

func (s *Server) getUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var u userRow
	err := s.pool.QueryRow(r.Context(),
		`SELECT `+userCols+` FROM users WHERE id = $1`, id).
		Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.Role,
			&u.Disabled, &u.LastLoginAt, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		Error(w, http.StatusNotFound, "user_not_found", "no such user")
		return
	}
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	JSON(w, http.StatusOK, u)
}

type updateUserRequest struct {
	DisplayName *string `json:"display_name"`
	Email       *string `json:"email"`
	Role        *string `json:"role"`
	Disabled    *bool   `json:"disabled"`
	Password    *string `json:"password"` // admin reset
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	actor := auth.UserFrom(r.Context())
	id := chi.URLParam(r, "id")

	var req updateUserRequest
	if err := Decode(w, r, &req); err != nil {
		Error(w, http.StatusBadRequest, "bad_request", "invalid request body")
		return
	}
	if req.Role != nil && !auth.ValidRole(*req.Role) {
		Error(w, http.StatusBadRequest, "invalid_role", "unknown role")
		return
	}

	// Guard: don't demote/disable the last admin.
	if (req.Role != nil && *req.Role != string(auth.RoleAdmin)) ||
		(req.Disabled != nil && *req.Disabled) {
		isAdmin, err := s.isAdmin(r, id)
		if err != nil {
			Error(w, http.StatusNotFound, "user_not_found", "no such user")
			return
		}
		if isAdmin {
			n := s.adminCount(r)
			if n <= 1 {
				Error(w, http.StatusConflict, "last_admin",
					"cannot demote or disable the last admin")
				return
			}
		}
	}

	if req.DisplayName != nil {
		if _, err := s.pool.Exec(r.Context(),
			`UPDATE users SET display_name = $1, updated_at = now() WHERE id = $2`,
			*req.DisplayName, id); err != nil {
			Error(w, http.StatusInternalServerError, "internal", "update failed")
			return
		}
	}
	if req.Email != nil {
		if _, err := s.pool.Exec(r.Context(),
			`UPDATE users SET email = NULLIF($1,''), updated_at = now() WHERE id = $2`,
			*req.Email, id); err != nil {
			Error(w, http.StatusConflict, "conflict", "email already in use")
			return
		}
	}
	if req.Role != nil {
		if _, err := s.pool.Exec(r.Context(),
			`UPDATE users SET role = $1, updated_at = now() WHERE id = $2`,
			*req.Role, id); err != nil {
			Error(w, http.StatusInternalServerError, "internal", "update failed")
			return
		}
		audit.Log(r.Context(), s.pool, audit.Entry{
			UserID: actor.ID, Action: "user.role_change", Target: id,
			Detail: map[string]any{"role": *req.Role}, IP: clientIP(r),
		})
	}
	if req.Disabled != nil {
		if _, err := s.pool.Exec(r.Context(),
			`UPDATE users SET disabled = $1, updated_at = now() WHERE id = $2`,
			*req.Disabled, id); err != nil {
			Error(w, http.StatusInternalServerError, "internal", "update failed")
			return
		}
		if *req.Disabled {
			// Kill sessions on disable.
			_, _ = s.pool.Exec(r.Context(), `DELETE FROM sessions WHERE user_id = $1`, id)
		}
	}
	if req.Password != nil {
		if !validatePassword(*req.Password, 8) {
			Error(w, http.StatusBadRequest, "weak_password", "password too short")
			return
		}
		hash, err := auth.HashPassword(*req.Password)
		if err != nil {
			Error(w, http.StatusInternalServerError, "internal", "could not hash password")
			return
		}
		if _, err := s.pool.Exec(r.Context(),
			`UPDATE users SET password_hash = $1, updated_at = now() WHERE id = $2`, hash, id); err != nil {
			Error(w, http.StatusInternalServerError, "internal", "update failed")
			return
		}
		_, _ = s.pool.Exec(r.Context(), `DELETE FROM sessions WHERE user_id = $1`, id)
	}

	audit.Log(r.Context(), s.pool, audit.Entry{
		UserID: actor.ID, Action: "user.update", Target: id, IP: clientIP(r),
	})
	s.getUser(w, r)
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	actor := auth.UserFrom(r.Context())
	id := chi.URLParam(r, "id")

	if id == actor.ID {
		Error(w, http.StatusConflict, "self_delete", "cannot delete your own account")
		return
	}
	isAdmin, err := s.isAdmin(r, id)
	if err != nil {
		Error(w, http.StatusNotFound, "user_not_found", "no such user")
		return
	}
	if isAdmin && s.adminCount(r) <= 1 {
		Error(w, http.StatusConflict, "last_admin", "cannot delete the last admin")
		return
	}

	if _, err := s.pool.Exec(r.Context(), `DELETE FROM users WHERE id = $1`, id); err != nil {
		Error(w, http.StatusInternalServerError, "internal", "delete failed")
		return
	}
	audit.Log(r.Context(), s.pool, audit.Entry{
		UserID: actor.ID, Action: "user.delete", Target: id, IP: clientIP(r),
	})
	JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) roles(w http.ResponseWriter, r *http.Request) {
	JSON(w, http.StatusOK, map[string]any{"roles": auth.Roles()})
}

func (s *Server) auditLog(w http.ResponseWriter, r *http.Request) {
	rows, err := s.pool.Query(r.Context(),
		`SELECT id, user_id, action, target, detail, ip, ts
		 FROM audit_log ORDER BY ts DESC LIMIT 200`)
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()

	type entry struct {
		ID     int64          `json:"id"`
		UserID *string        `json:"user_id"`
		Action string         `json:"action"`
		Target *string        `json:"target"`
		Detail map[string]any `json:"detail"`
		IP     *string        `json:"ip"`
		TS     time.Time      `json:"ts"`
	}
	entries := []entry{}
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.ID, &e.UserID, &e.Action, &e.Target, &e.Detail, &e.IP, &e.TS); err != nil {
			Error(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		entries = append(entries, e)
	}
	JSON(w, http.StatusOK, map[string]any{"entries": entries})
}

func (s *Server) isAdmin(r *http.Request, id string) (bool, error) {
	var role string
	err := s.pool.QueryRow(r.Context(),
		`SELECT role FROM users WHERE id = $1`, id).Scan(&role)
	if err != nil {
		return false, err
	}
	return role == string(auth.RoleAdmin), nil
}

func (s *Server) adminCount(r *http.Request) int {
	var n int
	_ = s.pool.QueryRow(r.Context(),
		`SELECT count(*) FROM users WHERE role = 'admin' AND NOT disabled`).Scan(&n)
	return n
}

func strOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
