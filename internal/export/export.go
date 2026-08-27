// Package export runs clip exports: resolve segments → rebuild a combined
// fMP4 (init + parts) → ffmpeg re-mux (-c copy) to a plain MP4.
// See docs/05-ingest-streaming.md (Exports).
package export

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sagostin/tapetum/internal/camera"
	"github.com/sagostin/tapetum/internal/record"
	"github.com/sagostin/tapetum/internal/storage"
	"github.com/sagostin/tapetum/internal/ws"
)

// ErrBusy means the user already has an active export.
var ErrBusy = errors.New("an export is already running for this user")

// Export is an exports table row.
type Export struct {
	ID        string    `json:"id"`
	CameraID  string    `json:"camera_id"`
	UserID    string    `json:"user_id"`
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	Status    string    `json:"status"`
	Path      *string   `json:"-"`
	SizeBytes *int64    `json:"size_bytes"`
	Error     *string   `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Worker processes the export queue.
type Worker struct {
	pool    *pgxpool.Pool
	segs    *record.Store
	cams    *camera.Store
	backend storage.Backend // local (write path for artifacts)
	resolve storage.Resolver
	hub     *ws.Hub
	dataDir string
	log     *slog.Logger

	sem chan struct{} // global concurrency cap
}

func NewWorker(pool *pgxpool.Pool, segs *record.Store, cams *camera.Store,
	backend storage.Backend, resolve storage.Resolver, hub *ws.Hub, dataDir string,
) *Worker {
	return &Worker{
		pool: pool, segs: segs, cams: cams, backend: backend, resolve: resolve, hub: hub,
		dataDir: dataDir,
		log:     slog.With("component", "export"),
		sem:     make(chan struct{}, 2),
	}
}

// Enqueue creates a pending export and starts processing in the background.
func (w *Worker) Enqueue(ctx context.Context, camID, userID string, start, end time.Time) (*Export, error) {
	if !end.After(start) {
		return nil, errors.New("end must be after start")
	}
	if end.Sub(start) > 24*time.Hour {
		return nil, errors.New("export range limited to 24h")
	}
	var active int
	if err := w.pool.QueryRow(ctx, `
		SELECT count(*) FROM exports
		WHERE user_id=$1 AND status IN ('pending','processing')`, userID).Scan(&active); err != nil {
		return nil, err
	}
	if active > 0 {
		return nil, ErrBusy
	}

	var e Export
	err := w.pool.QueryRow(ctx, `
		INSERT INTO exports (camera_id, user_id, start_ts, end_ts)
		VALUES ($1,$2,$3,$4)
		RETURNING id, camera_id, user_id, start_ts, end_ts, status, path, size_bytes, error, created_at`,
		camID, userID, start, end).
		Scan(&e.ID, &e.CameraID, &e.UserID, &e.Start, &e.End, &e.Status,
			&e.Path, &e.SizeBytes, &e.Error, &e.CreatedAt)
	if err != nil {
		return nil, err
	}

	go w.process(e.ID)
	return &e, nil
}

// List returns exports visible to the user (admin sees all).
func (w *Worker) List(ctx context.Context, userID string, admin bool) ([]*Export, error) {
	q := `SELECT id, camera_id, user_id, start_ts, end_ts, status, path, size_bytes, error, created_at
	      FROM exports`
	args := []any{}
	if !admin {
		q += ` WHERE user_id=$1`
		args = append(args, userID)
	}
	q += ` ORDER BY created_at DESC LIMIT 100`
	rows, err := w.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Export{}
	for rows.Next() {
		e := &Export{}
		if err := rows.Scan(&e.ID, &e.CameraID, &e.UserID, &e.Start, &e.End,
			&e.Status, &e.Path, &e.SizeBytes, &e.Error, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Get returns one export (ownership enforced by the handler).
func (w *Worker) Get(ctx context.Context, id string) (*Export, error) {
	e := &Export{}
	err := w.pool.QueryRow(ctx, `
		SELECT id, camera_id, user_id, start_ts, end_ts, status, path, size_bytes, error, created_at
		FROM exports WHERE id=$1`, id).
		Scan(&e.ID, &e.CameraID, &e.UserID, &e.Start, &e.End,
			&e.Status, &e.Path, &e.SizeBytes, &e.Error, &e.CreatedAt)
	if err != nil {
		return nil, err
	}
	return e, nil
}

func (w *Worker) setStatus(ctx context.Context, id, status string, path *string, size *int64, errStr *string) {
	_, err := w.pool.Exec(ctx, `
		UPDATE exports SET status=$2, path=$3, size_bytes=$4, error=$5 WHERE id=$1`,
		id, status, path, size, errStr)
	if err != nil {
		w.log.Warn("export status update", "id", id, "err", err)
	}
}

func (w *Worker) process(id string) {
	w.sem <- struct{}{}
	defer func() { <-w.sem }()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	e, err := w.Get(ctx, id)
	if err != nil {
		return
	}
	w.setStatus(ctx, id, "processing", nil, nil, nil)

	fail := func(err error) {
		msg := err.Error()
		w.log.Warn("export failed", "id", id, "err", msg)
		w.setStatus(ctx, id, "failed", nil, nil, &msg)
	}

	segs, err := w.segs.SegmentsInRange(ctx, e.CameraID, e.Start, e.End)
	if err != nil {
		fail(err)
		return
	}
	if len(segs) == 0 {
		fail(errors.New("no recordings in the requested range"))
		return
	}

	// init segment from the camera's recorded codec params
	cam, err := w.cams.Get(ctx, e.CameraID)
	if err != nil {
		fail(err)
		return
	}
	initB64, _ := cam.StatusDetail["init"].(string)
	if initB64 == "" {
		fail(errors.New("camera codec info unavailable"))
		return
	}
	initBytes, err := base64.StdEncoding.DecodeString(initB64)
	if err != nil {
		fail(err)
		return
	}

	// combined fMP4: init + each segment's moof/mdat (absolute tfdt keeps
	// the timeline continuous across gaps)
	tmp := filepath.Join(w.dataDir, "exports", id+".fmp4.tmp")
	if err := os.MkdirAll(filepath.Dir(tmp), 0o755); err != nil {
		fail(err)
		return
	}
	defer os.Remove(tmp)

	cf, err := os.Create(tmp)
	if err != nil {
		fail(err)
		return
	}
	if _, err := cf.Write(initBytes); err != nil {
		cf.Close()
		fail(err)
		return
	}
	for _, s := range segs {
		b, err := w.resolve(ctx, s.Storage)
		if err != nil {
			cf.Close()
			fail(fmt.Errorf("resolve tier %s: %w", s.Storage, err))
			return
		}
		rc, err := b.Open(ctx, s.Path)
		if err != nil {
			cf.Close()
			fail(fmt.Errorf("open segment %s: %w", s.ID, err))
			return
		}
		_, cpErr := copyWithContext(ctx, cf, rc)
		rc.Close()
		if cpErr != nil {
			cf.Close()
			fail(cpErr)
			return
		}
	}
	if err := cf.Close(); err != nil {
		fail(err)
		return
	}

	// re-mux to a plain faststart MP4
	outPath := filepath.Join("exports", id+".mp4")
	outAbs := filepath.Join(w.dataDir, outPath)
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-loglevel", "error", "-y",
		"-i", tmp,
		"-c", "copy", "-movflags", "+faststart", outAbs)
	var errb limitedBuffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		fail(fmt.Errorf("ffmpeg: %w (%s)", err, errb.String()))
		return
	}
	fi, err := os.Stat(outAbs)
	if err != nil {
		fail(err)
		return
	}
	size := fi.Size()
	w.setStatus(ctx, id, "done", &outPath, &size, nil)
	w.hub.Broadcast("export.done", map[string]any{
		"id": id, "camera_id": e.CameraID, "size_bytes": size,
	})
}

// File opens the exported MP4 for download.
func (w *Worker) File(ctx context.Context, e *Export) (*os.File, error) {
	if e.Path == nil {
		return nil, errors.New("export has no file")
	}
	p := filepath.Join(w.dataDir, *e.Path)
	if _, err := os.Stat(p); err != nil {
		return nil, err
	}
	return os.Open(p)
}

type limitedBuffer struct {
	b [4096]byte
	n int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if l.n < len(l.b) {
		l.n += copy(l.b[l.n:], p)
	}
	return len(p), nil
}

func (l *limitedBuffer) String() string { return string(l.b[:l.n]) }

// copyWithContext is io.Copy with cancellation polling.
func copyWithContext(ctx context.Context, dst *os.File, src io.Reader) (int64, error) {
	buf := make([]byte, 1<<20)
	var total int64
	for {
		if ctx.Err() != nil {
			return total, ctx.Err()
		}
		n, rerr := src.Read(buf)
		if n > 0 {
			m, werr := dst.Write(buf[:n])
			total += int64(m)
			if werr != nil {
				return total, werr
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return total, nil
			}
			return total, rerr
		}
	}
}
