package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Local stores objects under a root directory (data/).
type Local struct {
	root string
	// minFreeFrac is the disk safety valve: refuse writes below this free
	// fraction (docs/06-storage.md: 2%).
	minFreeFrac float64
}

// NewLocal creates a local backend rooted at root (created if missing).
func NewLocal(root string) (*Local, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("storage: mkdir %s: %w", root, err)
	}
	return &Local{root: root, minFreeFrac: 0.02}, nil
}

// path resolves a key to an absolute path, rejecting traversal.
func (l *Local) path(key string) (string, error) {
	if strings.Contains(key, "..") || strings.HasPrefix(key, "/") {
		return "", fmt.Errorf("storage: invalid key %q", key)
	}
	return filepath.Join(l.root, filepath.FromSlash(key)), nil
}

// FreeFrac returns the free fraction of the filesystem holding root.
func (l *Local) FreeFrac() float64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(l.root, &st); err != nil {
		return 1 // don't block writes if we can't measure
	}
	return float64(st.Bavail) / float64(st.Blocks)
}

func (l *Local) Put(ctx context.Context, key string, r io.Reader, _ int64) error {
	if l.FreeFrac() < l.minFreeFrac {
		return ErrDiskFull
	}
	p, err := l.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil { // fsync before index commit
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, p)
}

func (l *Local) Open(_ context.Context, key string) (io.ReadCloser, error) {
	p, err := l.path(key)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

func (l *Local) Presign(_ context.Context, _ string, _ time.Duration) (string, error) {
	return "", errors.New("storage: local backend does not presign")
}

func (l *Local) Delete(_ context.Context, key string) error {
	p, err := l.path(key)
	if err != nil {
		return err
	}
	err = os.Remove(p)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (l *Local) Stat(_ context.Context, key string) (ObjectInfo, error) {
	p, err := l.path(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	fi, err := os.Stat(p)
	if err != nil {
		return ObjectInfo{}, err
	}
	return ObjectInfo{Size: fi.Size()}, nil
}
