package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sagostin/tapetum/internal/auth"
)

// --- API tokens (own tokens; admin UI for all users comes later) ----------

type tokenRow struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Scopes     []string   `json:"scopes"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	rows, err := s.pool.Query(r.Context(),
		`SELECT id, name, scopes, expires_at, last_used_at, created_at
		 FROM api_tokens WHERE user_id = $1 ORDER BY created_at DESC`, u.ID)
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()

	tokens := []tokenRow{}
	for rows.Next() {
		var t tokenRow
		if err := rows.Scan(&t.ID, &t.Name, &t.Scopes, &t.ExpiresAt, &t.LastUsedAt, &t.CreatedAt); err != nil {
			Error(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		tokens = append(tokens, t)
	}
	JSON(w, http.StatusOK, map[string]any{"tokens": tokens})
}

type createTokenRequest struct {
	Name      string     `json:"name"`
	Scopes    []string   `json:"scopes"`
	ExpiresAt *time.Time `json:"expires_at"`
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	var req createTokenRequest
	if err := Decode(w, r, &req); err != nil {
		Error(w, http.StatusBadRequest, "bad_request", "invalid request body")
		return
	}
	if req.Name == "" || len(req.Name) > 64 {
		Error(w, http.StatusBadRequest, "invalid_name", "name is required (max 64 chars)")
		return
	}
	// Token scopes must be within the user's role.
	for _, scope := range req.Scopes {
		if !u.Role.Has(auth.Permission(scope)) {
			Error(w, http.StatusForbidden, "scope_exceeds_role",
				"scope "+scope+" is not granted by your role")
			return
		}
	}

	token, hash, err := auth.NewToken()
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", "could not generate token")
		return
	}
	if len(req.Scopes) == 0 {
		req.Scopes = []string{}
	}
	var id string
	err = s.pool.QueryRow(r.Context(),
		`INSERT INTO api_tokens (user_id, name, token_hash, scopes, expires_at)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		u.ID, req.Name, hash, req.Scopes, req.ExpiresAt).Scan(&id)
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", "could not store token")
		return
	}

	// The plaintext token is returned exactly once.
	JSON(w, http.StatusCreated, map[string]any{"id": id, "token": token})
}

func (s *Server) deleteToken(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	id := chi.URLParam(r, "id")
	tag, err := s.pool.Exec(r.Context(),
		`DELETE FROM api_tokens WHERE id = $1 AND user_id = $2`, id, u.ID)
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", "delete failed")
		return
	}
	if tag.RowsAffected() == 0 {
		Error(w, http.StatusNotFound, "token_not_found", "no such token")
		return
	}
	JSON(w, http.StatusOK, map[string]bool{"ok": true})
}
