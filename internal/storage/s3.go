package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/sagostin/tapetum/internal/settings"
)

// S3 stores segments in any S3-compatible object store (docs/06-storage.md).
// Reads by browsers never proxy through Go — they use Presign; Open exists
// for server-side consumers (tiering verify, exports).
type S3 struct {
	client       *minio.Client
	bucket       string
	prefix       string
	storageClass string
}

// NewS3 builds the backend from settings. cfg.Prefix is normalized to end
// with exactly one slash (or be empty).
func NewS3(cfg *settings.S3Config) (*S3, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" {
		return nil, errors.New("storage: s3 endpoint and bucket are required")
	}
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.Secure,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("storage: s3 client: %w", err)
	}
	prefix := strings.Trim(cfg.Prefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	return &S3{
		client:       client,
		bucket:       cfg.Bucket,
		prefix:       prefix,
		storageClass: cfg.StorageClass,
	}, nil
}

func (s *S3) objectKey(key string) string { return s.prefix + key }

// Ping verifies bucket access (used when saving settings).
func (s *S3) Ping(ctx context.Context) error {
	ok, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("storage: bucket %q does not exist", s.bucket)
	}
	return nil
}

func (s *S3) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	_, err := s.client.PutObject(ctx, s.bucket, s.objectKey(key), r, size,
		minio.PutObjectOptions{
			ContentType:  "video/mp4",
			StorageClass: s.storageClass,
		})
	return err
}

func (s *S3) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	return s.client.GetObject(ctx, s.bucket, s.objectKey(key), minio.GetObjectOptions{})
}

// Presign returns a temporary GET URL (docs/06-storage.md: 15-min TTL is
// chosen by the caller).
func (s *S3) Presign(ctx context.Context, key string, ttl time.Duration) (string, error) {
	u, err := s.client.PresignedGetObject(ctx, s.bucket, s.objectKey(key), ttl, nil)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

func (s *S3) Delete(ctx context.Context, key string) error {
	err := s.client.RemoveObject(ctx, s.bucket, s.objectKey(key), minio.RemoveObjectOptions{})
	if err != nil {
		errResp := minio.ToErrorResponse(err)
		if errResp.Code == "NoSuchKey" {
			return nil
		}
	}
	return err
}

func (s *S3) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	oi, err := s.client.StatObject(ctx, s.bucket, s.objectKey(key), minio.StatObjectOptions{})
	if err != nil {
		return ObjectInfo{}, err
	}
	return ObjectInfo{Size: oi.Size}, nil
}

// S3Manager lazily builds and caches the S3 backend from DB settings so
// config changes take effect without a restart.
type S3Manager struct {
	st *settings.Store

	mu     sync.Mutex
	cached *S3
	cfgTag string // fingerprint of the config the cache was built from
}

func NewS3Manager(st *settings.Store) *S3Manager {
	return &S3Manager{st: st}
}

// Backend returns the current S3 backend, or (nil, nil) when S3 is disabled.
func (m *S3Manager) Backend(ctx context.Context) (*S3, error) {
	cfg, err := m.st.GetS3(ctx)
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, nil
	}
	tag := fmt.Sprintf("%s|%v|%s|%s|%s|%s|%s",
		cfg.Endpoint, cfg.Secure, cfg.Region, cfg.Bucket, cfg.Prefix,
		cfg.AccessKey, cfg.StorageClass)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cached != nil && m.cfgTag == tag {
		return m.cached, nil
	}
	b, err := NewS3(cfg)
	if err != nil {
		return nil, err
	}
	m.cached = b
	m.cfgTag = tag
	return b, nil
}

// Resolver resolves a segment's storage tier to its backend.
type Resolver func(ctx context.Context, tier string) (Backend, error)

// NewResolver returns a Resolver over local + managed S3 backends.
func NewResolver(local Backend, s3m *S3Manager) Resolver {
	return func(ctx context.Context, tier string) (Backend, error) {
		switch tier {
		case "", "local":
			return local, nil
		case "s3":
			b, err := s3m.Backend(ctx)
			if err != nil {
				return nil, err
			}
			if b == nil {
				return nil, errors.New("storage: segment is in S3 but S3 is not configured")
			}
			return b, nil
		default:
			return nil, fmt.Errorf("storage: unknown tier %q", tier)
		}
	}
}
