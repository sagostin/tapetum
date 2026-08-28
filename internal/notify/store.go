// Package notify implements notification channels, the rules engine, and
// delivery with retry (docs/07-detection-notifications.md).
package notify

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sagostin/tapetum/internal/settings"
)

// Channel is one notification target; Config holds per-type fields with
// secrets stored encrypted under "*_enc" keys.
type Channel struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Type      string         `json:"type"`
	Config    map[string]any `json:"config"`
	Enabled   bool           `json:"enabled"`
	CreatedAt time.Time      `json:"created_at"`
}

// Rule matches events to channels.
type Rule struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Enabled    bool           `json:"enabled"`
	CameraIDs  []string       `json:"camera_ids"`
	EventTypes []string       `json:"event_types"`
	Labels     []string       `json:"labels"`
	Schedule   map[string]any `json:"schedule"`
	CooldownS  int            `json:"cooldown_s"`
	ChannelIDs []string       `json:"channel_ids"`
	CreatedAt  time.Time      `json:"created_at"`
}

// LogEntry is one delivery attempt outcome.
type LogEntry struct {
	ID        string     `json:"id"`
	RuleID    *string    `json:"rule_id"`
	ChannelID *string    `json:"channel_id"`
	EventTs   *time.Time `json:"event_ts"`
	EventID   *string    `json:"event_id"`
	Status    string     `json:"status"`
	Error     *string    `json:"error,omitempty"`
	SentAt    time.Time  `json:"sent_at"`
}

// secretKeys lists config fields encrypted at rest per channel type.
var secretKeys = map[string][]string{
	"smtp":     {"password"},
	"webhook":  {"hmac_secret"},
	"ntfy":     {"token"},
	"gotify":   {"token"},
	"telegram": {"bot_token"},
	"discord":  {"url"},
	"slack":    {"url"},
}

// ValidChannelType guards channel creation.
func ValidChannelType(t string) bool {
	_, ok := secretKeys[t]
	return ok
}

// Store persists channels/rules/log.
type Store struct {
	pool *pgxpool.Pool
	set  *settings.Store
}

func NewStore(pool *pgxpool.Pool, set *settings.Store) *Store {
	return &Store{pool: pool, set: set}
}

// encryptSecrets moves plaintext secret fields to their encrypted "*_enc"
// counterparts. Empty plaintext keeps the stored encrypted value.
func (s *Store) encryptSecrets(ch *Channel) error {
	for _, k := range secretKeys[ch.Type] {
		plain, _ := ch.Config[k].(string)
		if plain == "" {
			delete(ch.Config, k)
			continue
		}
		enc, err := s.set.Encrypt(plain)
		if err != nil {
			return err
		}
		ch.Config[k+"_enc"] = enc
		delete(ch.Config, k)
	}
	return nil
}

// decryptSecrets restores plaintext secrets (for sending).
func (s *Store) decryptSecrets(ch *Channel) error {
	for _, k := range secretKeys[ch.Type] {
		enc, _ := ch.Config[k+"_enc"].(string)
		if enc == "" {
			continue
		}
		plain, err := s.set.Decrypt(enc)
		if err != nil {
			return fmt.Errorf("decrypt %s: %w", k, err)
		}
		ch.Config[k] = plain
	}
	return nil
}

// maskSecrets removes secret material for API responses.
func maskSecrets(ch *Channel) {
	for _, k := range secretKeys[ch.Type] {
		if _, ok := ch.Config[k+"_enc"]; ok {
			ch.Config[k+"_enc"] = ""
		}
	}
}

func (s *Store) CreateChannel(ctx context.Context, ch *Channel) error {
	if err := s.encryptSecrets(ch); err != nil {
		return err
	}
	return s.pool.QueryRow(ctx, `
		INSERT INTO notification_channels (name, type, config, enabled)
		VALUES ($1,$2,$3,$4) RETURNING id, created_at`,
		ch.Name, ch.Type, ch.Config, ch.Enabled).Scan(&ch.ID, &ch.CreatedAt)
}

func (s *Store) UpdateChannel(ctx context.Context, ch *Channel) error {
	// merge: empty secret fields keep the previously stored values
	prev, err := s.getChannel(ctx, ch.ID, true)
	if err != nil {
		return err
	}
	if prev == nil {
		return pgx.ErrNoRows
	}
	for _, k := range secretKeys[ch.Type] {
		if v, _ := ch.Config[k].(string); v == "" {
			if enc, ok := prev.Config[k+"_enc"]; ok {
				ch.Config[k+"_enc"] = enc
			}
		}
	}
	if err := s.encryptSecrets(ch); err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE notification_channels SET name=$2, type=$3, config=$4, enabled=$5
		WHERE id=$1`, ch.ID, ch.Name, ch.Type, ch.Config, ch.Enabled)
	return err
}

func (s *Store) DeleteChannel(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM notification_channels WHERE id=$1`, id)
	return err
}

// getChannel loads one channel; withSecrets decrypts secret fields.
func (s *Store) getChannel(ctx context.Context, id string, withSecrets bool) (*Channel, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, type, config, enabled, created_at
		FROM notification_channels WHERE id=$1`, id)
	if err != nil {
		return nil, err
	}
	ch, err := pgx.CollectExactlyOneRow(rows, scanChannel)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if withSecrets {
		if err := s.decryptSecrets(ch); err != nil {
			return nil, err
		}
	} else {
		maskSecrets(ch)
	}
	return ch, nil
}

// GetChannel returns a channel masked for API responses.
func (s *Store) GetChannel(ctx context.Context, id string) (*Channel, error) {
	return s.getChannel(ctx, id, false)
}

// GetChannelForSend returns a channel with secrets decrypted.
func (s *Store) GetChannelForSend(ctx context.Context, id string) (*Channel, error) {
	return s.getChannel(ctx, id, true)
}

func (s *Store) ListChannels(ctx context.Context) ([]*Channel, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, type, config, enabled, created_at
		FROM notification_channels ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	chs, err := pgx.CollectRows(rows, scanChannel)
	for _, ch := range chs {
		maskSecrets(ch)
	}
	return chs, err
}

func scanChannel(row pgx.CollectableRow) (*Channel, error) {
	ch := &Channel{}
	return ch, row.Scan(&ch.ID, &ch.Name, &ch.Type, &ch.Config, &ch.Enabled, &ch.CreatedAt)
}

// --- rules ---

func (s *Store) CreateRule(ctx context.Context, r *Rule) error {
	return s.pool.QueryRow(ctx, `
		INSERT INTO notification_rules
			(name, enabled, camera_ids, event_types, labels, schedule, cooldown_s, channel_ids)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id, created_at`,
		r.Name, r.Enabled, r.CameraIDs, r.EventTypes, r.Labels, r.Schedule,
		r.CooldownS, r.ChannelIDs).Scan(&r.ID, &r.CreatedAt)
}

func (s *Store) UpdateRule(ctx context.Context, r *Rule) error {
	ct, err := s.pool.Exec(ctx, `
		UPDATE notification_rules SET name=$2, enabled=$3, camera_ids=$4,
			event_types=$5, labels=$6, schedule=$7, cooldown_s=$8, channel_ids=$9
		WHERE id=$1`, r.ID, r.Name, r.Enabled, r.CameraIDs, r.EventTypes,
		r.Labels, r.Schedule, r.CooldownS, r.ChannelIDs)
	if err == nil && ct.RowsAffected() == 0 {
		err = pgx.ErrNoRows
	}
	return err
}

func (s *Store) DeleteRule(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM notification_rules WHERE id=$1`, id)
	return err
}

func (s *Store) ListRules(ctx context.Context) ([]*Rule, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, enabled, camera_ids, event_types, labels, schedule,
		       cooldown_s, channel_ids, created_at
		FROM notification_rules ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (*Rule, error) {
		r := &Rule{}
		return r, row.Scan(&r.ID, &r.Name, &r.Enabled, &r.CameraIDs,
			&r.EventTypes, &r.Labels, &r.Schedule, &r.CooldownS, &r.ChannelIDs,
			&r.CreatedAt)
	})
}

// --- delivery log ---

func (s *Store) LogDelivery(ctx context.Context, e *LogEntry) error {
	return s.pool.QueryRow(ctx, `
		INSERT INTO notification_log (rule_id, channel_id, event_ts, event_id, status, error)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING id, sent_at`,
		e.RuleID, e.ChannelID, e.EventTs, e.EventID, e.Status, e.Error).
		Scan(&e.ID, &e.SentAt)
}

func (s *Store) ListLog(ctx context.Context, ruleID, status string, limit int) ([]*LogEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT id, rule_id, channel_id, event_ts, event_id, status, error, sent_at
		FROM notification_log WHERE true`
	args := []any{}
	if ruleID != "" {
		args = append(args, ruleID)
		q += fmt.Sprintf(" AND rule_id=$%d", len(args))
	}
	if status != "" {
		args = append(args, status)
		q += fmt.Sprintf(" AND status=$%d", len(args))
	}
	q += fmt.Sprintf(" ORDER BY sent_at DESC LIMIT %d", limit)
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (*LogEntry, error) {
		e := &LogEntry{}
		return e, row.Scan(&e.ID, &e.RuleID, &e.ChannelID, &e.EventTs,
			&e.EventID, &e.Status, &e.Error, &e.SentAt)
	})
}
