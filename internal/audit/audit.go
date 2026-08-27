// Package audit writes privileged mutations to the audit_log table.
package audit

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Entry struct {
	UserID string // empty for unauthenticated actions (e.g. failed setup)
	Action string // 'user.create', 'auth.login', …
	Target string // resource id, optional
	Detail map[string]any
	IP     string
}

// Log writes an audit entry. Failures are logged, never fatal.
func Log(ctx context.Context, pool *pgxpool.Pool, e Entry) {
	detail := e.Detail
	if detail == nil {
		detail = map[string]any{}
	}
	_, err := pool.Exec(ctx,
		`INSERT INTO audit_log (user_id, action, target, detail, ip)
		 VALUES (NULLIF($1,'')::uuid, $2, NULLIF($3,''), $4, NULLIF($5,'')::inet)`,
		e.UserID, e.Action, e.Target, detail, e.IP)
	if err != nil {
		slog.Warn("audit write failed", "action", e.Action, "err", err)
	}
}
