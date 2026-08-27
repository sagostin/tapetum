// Package record owns the recording pipeline: fMP4 segmentation, the
// Postgres segment index, gap tracking, and the retention janitor.
// See docs/05-ingest-streaming.md and docs/06-storage.md.
package record

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Segment is a recording_segments row.
type Segment struct {
	ID        string    `json:"id"`
	CameraID  string    `json:"camera_id"`
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	Storage   string    `json:"storage"`
	Path      string    `json:"path"`
	SizeBytes int64     `json:"size_bytes"`
	HasMotion bool      `json:"has_motion"`
	Protected bool      `json:"protected"`
}

// Gap is a recording_gaps row (end zero-value while open).
type Gap struct {
	ID       string     `json:"id"`
	CameraID string     `json:"camera_id"`
	Start    time.Time  `json:"start"`
	End      *time.Time `json:"end"`
	Reason   string     `json:"reason"`
}

var ErrSegmentNotFound = errors.New("segment not found")

// Store is the recording index accessor.
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

const segCols = `id, camera_id, start_ts, end_ts, storage, path, size_bytes, has_motion, protected`

func scanSegment(row pgx.Row) (*Segment, error) {
	var s Segment
	err := row.Scan(&s.ID, &s.CameraID, &s.Start, &s.End, &s.Storage,
		&s.Path, &s.SizeBytes, &s.HasMotion, &s.Protected)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSegmentNotFound
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (s *Store) InsertSegment(ctx context.Context, camID string, start, end time.Time, path string, size int64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO recording_segments (camera_id, start_ts, end_ts, path, size_bytes)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (camera_id, path) DO NOTHING`,
		camID, start, end, path, size)
	return err
}

func (s *Store) GetSegment(ctx context.Context, id string) (*Segment, error) {
	return scanSegment(s.pool.QueryRow(ctx,
		`SELECT `+segCols+` FROM recording_segments WHERE id=$1`, id))
}

// SegmentsInRange returns segments overlapping [from, to), ordered by start.
func (s *Store) SegmentsInRange(ctx context.Context, camID string, from, to time.Time) ([]*Segment, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+segCols+` FROM recording_segments
		WHERE camera_id=$1 AND start_ts < $3 AND end_ts > $2
		ORDER BY start_ts`, camID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Segment{}
	for rows.Next() {
		seg, err := scanSegment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, seg)
	}
	return out, rows.Err()
}

// RecentSegments returns the newest segments for the near-live playlist.
func (s *Store) RecentSegments(ctx context.Context, camID string, limit int) ([]*Segment, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+segCols+` FROM recording_segments
		WHERE camera_id=$1 ORDER BY start_ts DESC LIMIT $2`, camID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Segment{}
	for rows.Next() {
		seg, err := scanSegment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, seg)
	}
	// reverse to chronological
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

func (s *Store) DeleteSegment(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM recording_segments WHERE id=$1`, id)
	return err
}

// ListEvictable returns the oldest unprotected segments of a camera.
func (s *Store) ListEvictable(ctx context.Context, camID string, limit int) ([]*Segment, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+segCols+` FROM recording_segments
		WHERE camera_id=$1 AND NOT protected
		ORDER BY start_ts LIMIT $2`, camID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Segment{}
	for rows.Next() {
		seg, err := scanSegment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, seg)
	}
	return out, rows.Err()
}

// CameraBytes returns total local bytes recorded for a camera.
func (s *Store) CameraBytes(ctx context.Context, camID string) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(sum(size_bytes),0) FROM recording_segments
		WHERE camera_id=$1 AND storage='local'`, camID).Scan(&n)
	return n, err
}

// CameraIDsWithSegments returns distinct camera IDs present in the index.
func (s *Store) CameraIDsWithSegments(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT camera_id FROM recording_segments`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ProtectRange pins segments overlapping [start, end]; returns rows affected.
func (s *Store) ProtectRange(ctx context.Context, camID string, start, end time.Time) (int64, error) {
	ct, err := s.pool.Exec(ctx, `
		UPDATE recording_segments SET protected=true
		WHERE camera_id=$1 AND start_ts < $3 AND end_ts > $2`,
		camID, start, end)
	return ct.RowsAffected(), err
}

func (s *Store) Unprotect(ctx context.Context, id string) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE recording_segments SET protected=false WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrSegmentNotFound
	}
	return nil
}

// DeleteAllForCamera removes every segment row of a camera (delete_recordings).
func (s *Store) DeleteAllForCamera(ctx context.Context, camID string) ([]*Segment, error) {
	rows, err := s.pool.Query(ctx, `
		DELETE FROM recording_segments WHERE camera_id=$1
		RETURNING `+segCols, camID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Segment{}
	for rows.Next() {
		seg, err := scanSegment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, seg)
	}
	return out, rows.Err()
}

// OpenGap starts a recording gap (ingest failure).
func (s *Store) OpenGap(ctx context.Context, camID, reason string) error {
	// don't stack gaps: only open when none is open
	_, err := s.pool.Exec(ctx, `
		INSERT INTO recording_gaps (camera_id, start_ts, reason)
		SELECT $1, now(), $2
		WHERE NOT EXISTS (
			SELECT 1 FROM recording_gaps WHERE camera_id=$1 AND end_ts IS NULL)`,
		camID, reason)
	return err
}

// CloseGaps closes any open gap for the camera (ingest recovered).
func (s *Store) CloseGaps(ctx context.Context, camID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE recording_gaps SET end_ts=now()
		WHERE camera_id=$1 AND end_ts IS NULL`, camID)
	return err
}

// GapsInRange returns gaps overlapping [from, to).
func (s *Store) GapsInRange(ctx context.Context, camID string, from, to time.Time) ([]*Gap, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, camera_id, start_ts, end_ts, reason FROM recording_gaps
		WHERE camera_id=$1 AND start_ts < $3 AND (end_ts IS NULL OR end_ts > $2)
		ORDER BY start_ts`, camID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Gap{}
	for rows.Next() {
		g := &Gap{}
		if err := rows.Scan(&g.ID, &g.CameraID, &g.Start, &g.End, &g.Reason); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
