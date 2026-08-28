// Package settings is the typed accessor over the settings table
// (docs/02-data-model.md). Secrets (S3 secret key) are encrypted at rest
// with the server key.
package settings

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store reads/writes settings rows.
type Store struct {
	pool *pgxpool.Pool
	gcm  cipher.AEAD
}

// NewStore creates the store; serverKey encrypts secret fields at rest.
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

// Get unmarshals the value for key into v. found=false when the key is unset.
func (s *Store) Get(ctx context.Context, key string, v any) (found bool, err error) {
	var raw []byte
	err = s.pool.QueryRow(ctx, `SELECT value FROM settings WHERE key=$1`, key).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(raw, v)
}

// Set upserts key with v marshaled to JSON.
func (s *Store) Set(ctx context.Context, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO settings (key, value) VALUES ($1,$2)
		ON CONFLICT (key) DO UPDATE SET value=$2`, key, raw)
	return err
}

// Encrypt/Decrypt expose the at-rest secret encryption for other stores
// that keep secrets in JSON columns (notification channel configs).
func (s *Store) Encrypt(plain string) (string, error) { return s.encrypt(plain) }
func (s *Store) Decrypt(enc string) (string, error)   { return s.decrypt(enc) }

func (s *Store) encrypt(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	nonce := make([]byte, s.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(
		s.gcm.Seal(nonce, nonce, []byte(plain), nil)), nil
}

func (s *Store) decrypt(enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	ns := s.gcm.NonceSize()
	if len(raw) < ns {
		return "", errors.New("settings: corrupt encrypted value")
	}
	plain, err := s.gcm.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("settings: decrypt: %w", err)
	}
	return string(plain), nil
}

// S3Config configures the S3 cold tier (docs/06-storage.md). Endpoint is
// host[:port] without a scheme; Secure selects https.
type S3Config struct {
	Enabled      bool   `json:"enabled"`
	Endpoint     string `json:"endpoint"`
	Secure       bool   `json:"secure"`
	Region       string `json:"region"`
	Bucket       string `json:"bucket"`
	Prefix       string `json:"prefix"`
	AccessKey    string `json:"access_key"`
	StorageClass string `json:"storage_class,omitempty"`

	// SecretKey is never serialized to clients; at rest it is stored
	// encrypted in SecretKeyEnc.
	SecretKey    string `json:"-"`
	SecretKeyEnc string `json:"secret_key_enc,omitempty"`
}

// GetS3 returns the S3 config (empty, disabled when unset) with the secret
// key decrypted.
func (s *Store) GetS3(ctx context.Context) (*S3Config, error) {
	cfg := &S3Config{}
	found, err := s.Get(ctx, "s3", cfg)
	if err != nil || !found {
		return cfg, err
	}
	cfg.SecretKey, err = s.decrypt(cfg.SecretKeyEnc)
	if err != nil {
		return nil, err
	}
	cfg.SecretKeyEnc = ""
	return cfg, nil
}

// SetS3 persists the S3 config, encrypting the secret key. An empty
// SecretKey keeps the previously stored one (write-only field semantics).
func (s *Store) SetS3(ctx context.Context, cfg *S3Config) error {
	if cfg.SecretKey == "" {
		prev, err := s.GetS3(ctx)
		if err != nil {
			return err
		}
		cfg.SecretKey = prev.SecretKey
	}
	enc, err := s.encrypt(cfg.SecretKey)
	if err != nil {
		return err
	}
	cfg.SecretKeyEnc = enc
	return s.Set(ctx, "s3", cfg)
}
