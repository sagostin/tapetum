package events

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the event persistence seam (docs/01-architecture.md). The
// Postgres implementation is the default; a ClickHouse implementation can
// take over detection telemetry in phase 4 without touching callers.
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Event is one row of the events table.
type Event struct {
	ID           string         `json:"id"`
	CameraID     string         `json:"camera_id"`
	Ts           time.Time      `json:"ts"`
	EndTs        *time.Time     `json:"end_ts,omitempty"`
	Type         string         `json:"type"`
	Label        *string        `json:"label,omitempty"`
	Confidence   *float32       `json:"confidence,omitempty"`
	Bbox         map[string]any `json:"bbox,omitempty"`
	SnapshotPath *string        `json:"-"`
	ClipStart    *time.Time     `json:"clip_start,omitempty"`
	ClipEnd      *time.Time     `json:"clip_end,omitempty"`
	NotifiedAt   *time.Time     `json:"notified_at,omitempty"`
	AckedBy      *string        `json:"acked_by,omitempty"`
	AckedAt      *time.Time     `json:"acked_at,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

// ListFilter selects events for the feed (cursor = exclusive upper bound on
// (ts,id) for keyset pagination).
type ListFilter struct {
	CameraID string
	Type     string
	Label    string
	From, To time.Time
	Unacked  bool
	Limit    int
	CursorTs time.Time
	CursorID string
}

// Insert creates an open event row.
func (s *Store) Insert(ctx context.Context, e *Event) error {
	return s.pool.QueryRow(ctx, `
		INSERT INTO events (camera_id, ts, type, label, confidence, bbox,
		                    snapshot_path, clip_start_ts, clip_end_ts, metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		RETURNING id`,
		e.CameraID, e.Ts, e.Type, e.Label, e.Confidence, e.Bbox,
		e.SnapshotPath, e.ClipStart, e.ClipEnd, e.Metadata).Scan(&e.ID)
}

// Close fills end_ts on an open event.
func (s *Store) Close(ctx context.Context, id string, end time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE events SET end_ts=$2 WHERE id=$1`, id, end)
	return err
}

// SetSnapshot attaches the snapshot storage key.
func (s *Store) SetSnapshot(ctx context.Context, id, path string) error {
	_, err := s.pool.Exec(ctx, `UPDATE events SET snapshot_path=$2 WHERE id=$1`, id, path)
	return err
}

// SetClip stores the clip playback range (event window + pre/post roll).
func (s *Store) SetClip(ctx context.Context, id string, start, end time.Time) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE events SET clip_start_ts=$2, clip_end_ts=$3 WHERE id=$1`, id, start, end)
	return err
}

// MarkNotified stamps the first successful notification time.
func (s *Store) MarkNotified(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE events SET notified_at=now() WHERE id=$1 AND notified_at IS NULL`, id)
	return err
}

// SetMetadata replaces the metadata blob.
func (s *Store) SetMetadata(ctx context.Context, id string, md map[string]any) error {
	_, err := s.pool.Exec(ctx, `UPDATE events SET metadata=$2 WHERE id=$1`, id, md)
	return err
}

// Ack acknowledges an event on behalf of a user.
func (s *Store) Ack(ctx context.Context, id, userID string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE events SET acked_by=$2, acked_at=now() WHERE id=$1`, id, userID)
	if err == nil && tag.RowsAffected() == 0 {
		err = pgx.ErrNoRows
	}
	return err
}

// Get returns one event by id.
func (s *Store) Get(ctx context.Context, id string) (*Event, error) {
	rows, err := s.pool.Query(ctx, eventSelect+` WHERE id=$1`, id)
	if err != nil {
		return nil, err
	}
	e, err := pgx.CollectExactlyOneRow(rows, scanEvent)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return e, err
}

const eventSelect = `
	SELECT id, camera_id, ts, end_ts, type, label, confidence, bbox,
	       snapshot_path, clip_start_ts, clip_end_ts, notified_at,
	       acked_by, acked_at, metadata FROM events`

func scanEvent(row pgx.CollectableRow) (*Event, error) {
	e := &Event{}
	err := row.Scan(&e.ID, &e.CameraID, &e.Ts, &e.EndTs, &e.Type, &e.Label,
		&e.Confidence, &e.Bbox, &e.SnapshotPath, &e.ClipStart, &e.ClipEnd,
		&e.NotifiedAt, &e.AckedBy, &e.AckedAt, &e.Metadata)
	return e, err
}

// List returns events newest-first plus a cursor for the next page
// (empty when the page is full-size-exhausted).
func (s *Store) List(ctx context.Context, f ListFilter) ([]*Event, string, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	q := eventSelect + ` WHERE true`
	args := []any{}
	add := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if f.CameraID != "" {
		q += ` AND camera_id=` + add(f.CameraID)
	}
	if f.Type != "" {
		q += ` AND type=` + add(f.Type)
	}
	if f.Label != "" {
		q += ` AND label=` + add(f.Label)
	}
	if !f.From.IsZero() {
		q += ` AND ts>=` + add(f.From)
	}
	if !f.To.IsZero() {
		q += ` AND ts<=` + add(f.To)
	}
	if f.Unacked {
		q += ` AND acked_at IS NULL`
	}
	if !f.CursorTs.IsZero() {
		q += ` AND (ts,id) < (` + add(f.CursorTs) + `,` + add(f.CursorID) + `)`
	}
	q += ` ORDER BY ts DESC, id DESC LIMIT ` + add(f.Limit+1)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	events, err := pgx.CollectRows(rows, scanEvent)
	if err != nil {
		return nil, "", err
	}
	cursor := ""
	if len(events) > f.Limit {
		last := events[f.Limit-1]
		cursor = last.Ts.Format(time.RFC3339Nano) + "|" + last.ID
		events = events[:f.Limit]
	}
	return events, cursor, nil
}

// Delete removes an event and returns it (for artifact cleanup).
func (s *Store) Delete(ctx context.Context, id string) (*Event, error) {
	e, err := s.Get(ctx, id)
	if err != nil || e == nil {
		return e, err
	}
	_, err = s.pool.Exec(ctx, `DELETE FROM events WHERE id=$1`, id)
	return e, err
}

// TimelineEvent is the compact marker shape for the timeline response.
type TimelineEvent struct {
	ID    string  `json:"id"`
	Ts    string  `json:"ts"`
	Type  string  `json:"type"`
	Label *string `json:"label,omitempty"`
}

// InRange returns event markers for the timeline scrubber.
func (s *Store) InRange(ctx context.Context, camID string, from, to time.Time, limit int) ([]*TimelineEvent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, ts, type, label FROM events
		WHERE camera_id=$1 AND ts BETWEEN $2 AND $3
		ORDER BY ts LIMIT $4`, camID, from, to, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (*TimelineEvent, error) {
		te := &TimelineEvent{}
		var ts time.Time
		if err := row.Scan(&te.ID, &ts, &te.Type, &te.Label); err != nil {
			return nil, err
		}
		te.Ts = ts.Format(time.RFC3339Nano)
		return te, nil
	})
}

// Density computes per-bucket motion intensity 0–1: the fraction of each
// bucket covered by motion event windows. Buckets span [from,to).
func (s *Store) Density(ctx context.Context, camID string, from, to time.Time, buckets int) ([]float64, error) {
	if buckets <= 0 {
		buckets = 200
	}
	rows, err := s.pool.Query(ctx, `
		SELECT ts, COALESCE(end_ts, ts + interval '5 seconds') FROM events
		WHERE camera_id=$1 AND type IN ('motion','ai') AND ts < $3
		  AND COALESCE(end_ts, ts + interval '5 seconds') > $2`,
		camID, from, to)
	if err != nil {
		return nil, err
	}
	type span struct{ a, b time.Time }
	var spans []span
	var a, b time.Time
	if _, err := pgx.ForEachRow(rows, []any{&a, &b}, func() error {
		spans = append(spans, span{a, b})
		return nil
	}); err != nil {
		return nil, err
	}
	out := make([]float64, buckets)
	total := to.Sub(from)
	if total <= 0 {
		return out, nil
	}
	bucketDur := total / time.Duration(buckets)
	for _, sp := range spans {
		lo := int(sp.a.Sub(from) / bucketDur)
		hi := int(sp.b.Sub(from) / bucketDur)
		if hi >= buckets {
			hi = buckets - 1
		}
		for i := max(lo, 0); i <= hi; i++ {
			bs := from.Add(time.Duration(i) * bucketDur)
			ov := minTime(sp.b, bs.Add(bucketDur)).Sub(maxTime(sp.a, bs))
			if ov > 0 {
				out[i] = min(1.0, out[i]+float64(ov)/float64(bucketDur))
			}
		}
	}
	return out, nil
}

// CloseAllOpen closes events left open by a previous daemon run (crash or
// shutdown mid-event). Called once at boot, before detectors start.
func (s *Store) CloseAllOpen(ctx context.Context) (int64, error) {
	ct, err := s.pool.Exec(ctx, `
		UPDATE events SET end_ts = GREATEST(ts, now() - interval '1 second'),
		                  clip_end_ts = GREATEST(ts, now() - interval '1 second')
		WHERE end_ts IS NULL`)
	return ct.RowsAffected(), err
}

// EnsurePartitions creates monthly partitions for the current and next
// month. Idempotent; called at boot and daily.
func (s *Store) EnsurePartitions(ctx context.Context) error {
	now := time.Now().UTC()
	for _, m := range []time.Time{now, now.AddDate(0, 1, 0)} {
		start := time.Date(m.Year(), m.Month(), 1, 0, 0, 0, 0, time.UTC)
		end := start.AddDate(0, 1, 0)
		name := fmt.Sprintf("events_%04d_%02d", start.Year(), int(start.Month()))
		if _, err := s.pool.Exec(ctx, fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS %s PARTITION OF events
			FOR VALUES FROM ('%s') TO ('%s')`,
			name, start.Format("2006-01-02"), end.Format("2006-01-02"))); err != nil {
			return err
		}
	}
	return nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
