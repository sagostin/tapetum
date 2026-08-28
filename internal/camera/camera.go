// Package camera is the camera model and Postgres store.
// See docs/02-data-model.md (cameras table).
package camera

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Status is the live ingest status.
type Status string

const (
	StatusOnline   Status = "online"
	StatusOffline  Status = "offline"
	StatusDegraded Status = "degraded"
)

// Camera is the cameras table row. PasswordEnc holds the RTSP password
// encrypted with AES-GCM under the server key (never serialized to JSON).
type Camera struct {
	ID                string         `json:"id"`
	Name              string         `json:"name"`
	Enabled           bool           `json:"enabled"`
	MainURL           string         `json:"main_url"`
	SubURL            *string        `json:"sub_url"`
	Username          string         `json:"username"`
	PasswordEnc       []byte         `json:"-"`
	Transport         string         `json:"transport"`
	OnvifEndpoint     *string        `json:"onvif_endpoint"`
	OnvifProfile      *string        `json:"onvif_profile"`
	HasPTZ            bool           `json:"has_ptz"`
	RecordMode        string         `json:"record_mode"`
	RetentionDays     int            `json:"retention_days"`
	RetentionGB       *int           `json:"retention_gb"`
	TierAfterDays     *int           `json:"tier_after_days"`
	MotionConfig      map[string]any `json:"motion_config"`
	AIConfig          map[string]any `json:"ai_config"`
	GroupID           *string        `json:"group_id"`
	PlaybackTranscode string         `json:"playback_transcode"`
	ImagingConfig     map[string]any `json:"imaging_config"`
	DisplayRotate     int            `json:"display_rotate"`
	DisplayHFlip      bool           `json:"display_hflip"`
	DisplayVFlip      bool           `json:"display_vflip"`
	Status            Status         `json:"status"`
	StatusDetail      map[string]any `json:"status_detail"`
	LastSeenAt        *time.Time     `json:"last_seen_at"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
}

var ErrNotFound = errors.New("camera not found")

// Store is the cameras table accessor.
type Store struct {
	pool *pgxpool.Pool
	gcm  cipher.AEAD
}

// NewStore creates the store; key is the 32-byte server key used to
// encrypt camera passwords at rest.
func NewStore(pool *pgxpool.Pool, serverKey []byte) (*Store, error) {
	block, err := aes.NewCipher(serverKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Store{pool: pool, gcm: gcm}, nil
}

// EncryptPassword encrypts a plaintext RTSP password for password_enc.
func (s *Store) EncryptPassword(plain string) ([]byte, error) {
	if plain == "" {
		return nil, nil
	}
	nonce := make([]byte, s.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.gcm.Seal(nonce, nonce, []byte(plain), nil), nil
}

// DecryptPassword decrypts password_enc.
func (s *Store) DecryptPassword(enc []byte) (string, error) {
	if len(enc) == 0 {
		return "", nil
	}
	ns := s.gcm.NonceSize()
	if len(enc) < ns {
		return "", errors.New("camera: corrupt password_enc")
	}
	plain, err := s.gcm.Open(nil, enc[:ns], enc[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("camera: decrypt password: %w", err)
	}
	return string(plain), nil
}

const columns = `id, name, enabled, main_url, sub_url, username, password_enc,
	transport, onvif_endpoint, onvif_profile, has_ptz, record_mode,
	retention_days, retention_gb, tier_after_days, motion_config, ai_config,
	group_id, playback_transcode, imaging_config,
	display_rotate, display_hflip, display_vflip,
	status, status_detail, last_seen_at, created_at, updated_at`

func (s *Store) scan(row pgx.Row) (*Camera, error) {
	var c Camera
	err := row.Scan(&c.ID, &c.Name, &c.Enabled, &c.MainURL, &c.SubURL,
		&c.Username, &c.PasswordEnc, &c.Transport, &c.OnvifEndpoint,
		&c.OnvifProfile, &c.HasPTZ, &c.RecordMode, &c.RetentionDays,
		&c.RetentionGB, &c.TierAfterDays, &c.MotionConfig, &c.AIConfig,
		&c.GroupID, &c.PlaybackTranscode, &c.ImagingConfig,
		&c.DisplayRotate, &c.DisplayHFlip, &c.DisplayVFlip,
		&c.Status, &c.StatusDetail, &c.LastSeenAt, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) Get(ctx context.Context, id string) (*Camera, error) {
	return s.scan(s.pool.QueryRow(ctx,
		`SELECT `+columns+` FROM cameras WHERE id=$1`, id))
}

// List returns all cameras; when accessUserID is non-empty and the user has
// user_camera_access rows, the list is restricted to those cameras.
func (s *Store) List(ctx context.Context, accessUserID string) ([]*Camera, error) {
	q := `SELECT ` + columns + ` FROM cameras`
	args := []any{}
	if accessUserID != "" {
		q += ` WHERE (SELECT count(*) FROM user_camera_access WHERE user_id=$1) = 0
		       OR id IN (SELECT camera_id FROM user_camera_access WHERE user_id=$1)`
		args = append(args, accessUserID)
	}
	q += ` ORDER BY name`
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Camera{}
	for rows.Next() {
		c, err := s.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CanAccess reports whether the user may see the camera (empty ACL = all).
func (s *Store) CanAccess(ctx context.Context, userID, cameraID string) bool {
	var ok bool
	err := s.pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM user_camera_access WHERE user_id=$1) = 0
		    OR EXISTS (SELECT 1 FROM user_camera_access WHERE user_id=$1 AND camera_id=$2)`,
		userID, cameraID).Scan(&ok)
	return err == nil && ok
}

// ListEnabled returns enabled cameras (for ingest startup).
func (s *Store) ListEnabled(ctx context.Context) ([]*Camera, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+columns+` FROM cameras WHERE enabled ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Camera{}
	for rows.Next() {
		c, err := s.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type CreateParams struct {
	Name              string
	MainURL           string
	SubURL            *string
	Username          string
	Password          string // plaintext; encrypted before insert
	Transport         string
	OnvifEndpoint     *string
	OnvifProfile      *string
	HasPTZ            bool
	RecordMode        string
	RetentionDays     int
	RetentionGB       *int
	TierAfterDays     *int
	PlaybackTranscode string
	GroupID           *string
	MotionConfig      map[string]any
	AIConfig          map[string]any
}

func (s *Store) Create(ctx context.Context, p CreateParams) (*Camera, error) {
	enc, err := s.EncryptPassword(p.Password)
	if err != nil {
		return nil, err
	}
	transcode := p.PlaybackTranscode
	if transcode == "" {
		transcode = "auto"
	}
	motionCfg := p.MotionConfig
	if motionCfg == nil {
		motionCfg = map[string]any{}
	}
	aiCfg := p.AIConfig
	if aiCfg == nil {
		aiCfg = map[string]any{}
	}
	return s.scan(s.pool.QueryRow(ctx, `
		INSERT INTO cameras (name, main_url, sub_url, username, password_enc,
		                     transport, onvif_endpoint, onvif_profile, has_ptz,
		                     record_mode, retention_days, retention_gb,
		                     tier_after_days, playback_transcode, group_id,
		                     motion_config, ai_config)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		RETURNING `+columns,
		p.Name, p.MainURL, p.SubURL, p.Username, enc, p.Transport,
		p.OnvifEndpoint, p.OnvifProfile, p.HasPTZ, p.RecordMode,
		p.RetentionDays, p.RetentionGB, p.TierAfterDays, transcode, p.GroupID,
		motionCfg, aiCfg))
}

// UpdateParams carries optional field updates; nil pointer = leave unchanged.
type UpdateParams struct {
	Name              *string
	MainURL           *string
	SubURL            *string // empty string clears
	Username          *string
	Password          *string // empty string clears
	Transport         *string
	OnvifEndpoint     *string // empty string clears
	OnvifProfile      *string
	HasPTZ            *bool
	RecordMode        *string
	RetentionDays     *int
	RetentionGB       *int // nil pointer ambiguity handled by handler via ClearRetentionGB
	TierAfterDays     *int
	PlaybackTranscode *string
	GroupID           *string
	MotionConfig      map[string]any
	AIConfig          map[string]any
	ImagingConfig     map[string]any
	DisplayRotate     *int
	DisplayHFlip      *bool
	DisplayVFlip      *bool
}

func (s *Store) Update(ctx context.Context, id string, p UpdateParams) (*Camera, error) {
	cur, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.Name != nil {
		cur.Name = *p.Name
	}
	if p.MainURL != nil {
		cur.MainURL = *p.MainURL
	}
	if p.SubURL != nil {
		if *p.SubURL == "" {
			cur.SubURL = nil
		} else {
			cur.SubURL = p.SubURL
		}
	}
	if p.Username != nil {
		cur.Username = *p.Username
	}
	if p.Password != nil {
		enc, err := s.EncryptPassword(*p.Password)
		if err != nil {
			return nil, err
		}
		cur.PasswordEnc = enc
	}
	if p.Transport != nil {
		cur.Transport = *p.Transport
	}
	if p.OnvifEndpoint != nil {
		if *p.OnvifEndpoint == "" {
			cur.OnvifEndpoint = nil
			cur.OnvifProfile = nil
			cur.HasPTZ = false
		} else {
			cur.OnvifEndpoint = p.OnvifEndpoint
		}
	}
	if p.OnvifProfile != nil {
		cur.OnvifProfile = p.OnvifProfile
	}
	if p.HasPTZ != nil {
		cur.HasPTZ = *p.HasPTZ
	}
	if p.RecordMode != nil {
		cur.RecordMode = *p.RecordMode
	}
	if p.RetentionDays != nil {
		cur.RetentionDays = *p.RetentionDays
	}
	if p.RetentionGB != nil {
		cur.RetentionGB = p.RetentionGB
	}
	if p.TierAfterDays != nil {
		cur.TierAfterDays = p.TierAfterDays
	}
	if p.PlaybackTranscode != nil {
		cur.PlaybackTranscode = *p.PlaybackTranscode
	}
	if p.GroupID != nil {
		cur.GroupID = p.GroupID
	}
	if p.MotionConfig != nil {
		cur.MotionConfig = p.MotionConfig
	}
	if p.AIConfig != nil {
		cur.AIConfig = p.AIConfig
	}
	if p.ImagingConfig != nil {
		cur.ImagingConfig = p.ImagingConfig
	}
	if p.DisplayRotate != nil {
		cur.DisplayRotate = *p.DisplayRotate
	}
	if p.DisplayHFlip != nil {
		cur.DisplayHFlip = *p.DisplayHFlip
	}
	if p.DisplayVFlip != nil {
		cur.DisplayVFlip = *p.DisplayVFlip
	}
	return s.scan(s.pool.QueryRow(ctx, `
		UPDATE cameras SET name=$2, main_url=$3, sub_url=$4, username=$5,
			password_enc=$6, transport=$7, record_mode=$8, retention_days=$9,
			retention_gb=$10, group_id=$11, motion_config=$12, ai_config=$13,
			onvif_endpoint=$14, onvif_profile=$15, has_ptz=$16,
			tier_after_days=$17, playback_transcode=$18, imaging_config=$19,
			display_rotate=$20, display_hflip=$21, display_vflip=$22,
			updated_at=now()
		WHERE id=$1 RETURNING `+columns,
		id, cur.Name, cur.MainURL, cur.SubURL, cur.Username, cur.PasswordEnc,
		cur.Transport, cur.RecordMode, cur.RetentionDays, cur.RetentionGB,
		cur.GroupID, cur.MotionConfig, cur.AIConfig, cur.OnvifEndpoint,
		cur.OnvifProfile, cur.HasPTZ, cur.TierAfterDays, cur.PlaybackTranscode,
		cur.ImagingConfig, cur.DisplayRotate, cur.DisplayHFlip, cur.DisplayVFlip))
}

func (s *Store) Delete(ctx context.Context, id string) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM cameras WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetEnabled(ctx context.Context, id string, enabled bool) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE cameras SET enabled=$2, updated_at=now() WHERE id=$1`, id, enabled)
	return err
}

// SetStatus updates the live status + detail blob (called by ingest workers).
func (s *Store) SetStatus(ctx context.Context, id string, status Status, detail map[string]any) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE cameras SET status=$2, status_detail=$3,
		       last_seen_at = CASE WHEN $2='online' THEN now() ELSE last_seen_at END
		WHERE id=$1`, id, string(status), detail)
	return err
}
