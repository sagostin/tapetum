// Package config loads Tapetum configuration from defaults, an optional YAML
// file, and environment variable overrides (TAPETUM_*).
package config

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	Log      LogConfig      `yaml:"log"`
}

type ServerConfig struct {
	Addr      string `yaml:"addr"`       // listen address, default :8080
	PublicURL string `yaml:"public_url"` // external URL used in notification links
	DataDir   string `yaml:"data_dir"`   // default ./data
	Dev       bool   `yaml:"dev"`        // relaxes Secure cookie flag for http://localhost
	PprofAddr string `yaml:"pprof_addr"` // pprof listen address (e.g. ":6060"); empty = disabled
}

type DatabaseConfig struct {
	URL string `yaml:"url"` // postgres://user:pass@host:5432/tapetum
}

type LogConfig struct {
	Level string `yaml:"level"` // debug, info, warn, error
}

// Default returns the built-in defaults.
func Default() *Config {
	c := &Config{}
	c.Server.Addr = ":8080"
	c.Server.DataDir = "./data"
	c.Database.URL = "postgres://tapetum:tapetum@localhost:5432/tapetum?sslmode=disable"
	c.Log.Level = "info"
	return c
}

// Load applies: defaults → YAML file (if it exists) → env overrides.
func Load(path string) (*Config, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
		// fine — defaults + env only
	default:
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	applyEnv(cfg)
	return cfg, nil
}

func applyEnv(c *Config) {
	set := func(key string, dst *string) {
		if v, ok := os.LookupEnv(key); ok {
			*dst = v
		}
	}
	set("TAPETUM_ADDR", &c.Server.Addr)
	set("TAPETUM_PUBLIC_URL", &c.Server.PublicURL)
	set("TAPETUM_DATA_DIR", &c.Server.DataDir)
	set("TAPETUM_DATABASE_URL", &c.Database.URL)
	set("TAPETUM_LOG_LEVEL", &c.Log.Level)
	set("TAPETUM_PPROF_ADDR", &c.Server.PprofAddr)
	if v, ok := os.LookupEnv("TAPETUM_DEV"); ok {
		c.Server.Dev = v == "1" || strings.EqualFold(v, "true")
	}
}

// ServerKey returns the 32-byte server key used to encrypt secrets at rest
// (camera passwords, notification channel secrets). It comes from
// TAPETUM_SECRET_KEY (64 hex chars) or is generated once and stored in
// <data_dir>/server.key with mode 0600.
func (c *Config) ServerKey() ([]byte, error) {
	if v, ok := os.LookupEnv("TAPETUM_SECRET_KEY"); ok {
		if len(v) != 64 {
			return nil, fmt.Errorf("TAPETUM_SECRET_KEY must be 64 hex chars (32 bytes)")
		}
		key := make([]byte, 32)
		for i := range key {
			var b int
			if _, err := fmt.Sscanf(v[i*2:i*2+2], "%02x", &b); err != nil {
				return nil, fmt.Errorf("TAPETUM_SECRET_KEY: invalid hex: %w", err)
			}
			key[i] = byte(b)
		}
		return key, nil
	}

	path := filepath.Join(c.Server.DataDir, "server.key")
	if data, err := os.ReadFile(path); err == nil {
		if len(data) != 32 {
			return nil, fmt.Errorf("%s: expected 32 bytes, got %d", path, len(data))
		}
		return data, nil
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(c.Server.DataDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	return key, nil
}
