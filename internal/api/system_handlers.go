package api

import (
	"net/http"
	"time"

	"github.com/sagostin/tapetum/internal/audit"
	"github.com/sagostin/tapetum/internal/auth"
	"github.com/sagostin/tapetum/internal/settings"
	"github.com/sagostin/tapetum/internal/storage"
)

// --- system ---------------------------------------------------------------

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	JSON(w, http.StatusOK, map[string]any{
		"version":    s.version,
		"uptime_s":   int(time.Since(s.started).Seconds()),
		"ws_clients": s.hub.Count(),
		"public_url": s.cfg.Server.PublicURL,
	})
}

// --- settings -------------------------------------------------------------

// getSettings returns instance settings with secrets masked (settings:write).
func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	s3, err := s.settings.GetS3(r.Context())
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	JSON(w, http.StatusOK, map[string]any{
		"s3": map[string]any{
			"enabled":       s3.Enabled,
			"endpoint":      s3.Endpoint,
			"secure":        s3.Secure,
			"region":        s3.Region,
			"bucket":        s3.Bucket,
			"prefix":        s3.Prefix,
			"access_key":    s3.AccessKey,
			"storage_class": s3.StorageClass,
			"secret_set":    s3.SecretKey != "",
		},
	})
}

// putSettings updates instance settings. The S3 secret is write-only: omit
// secret_key to keep the stored one. When enabled, the bucket is verified.
func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var b struct {
		S3 *struct {
			Enabled      bool   `json:"enabled"`
			Endpoint     string `json:"endpoint"`
			Secure       bool   `json:"secure"`
			Region       string `json:"region"`
			Bucket       string `json:"bucket"`
			Prefix       string `json:"prefix"`
			AccessKey    string `json:"access_key"`
			SecretKey    string `json:"secret_key"`
			StorageClass string `json:"storage_class"`
		} `json:"s3"`
	}
	if err := Decode(w, r, &b); err != nil {
		Error(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if b.S3 == nil {
		Error(w, http.StatusBadRequest, "bad_request", "no settings in body")
		return
	}
	cfg := &settings.S3Config{
		Enabled:      b.S3.Enabled,
		Endpoint:     b.S3.Endpoint,
		Secure:       b.S3.Secure,
		Region:       b.S3.Region,
		Bucket:       b.S3.Bucket,
		Prefix:       b.S3.Prefix,
		AccessKey:    b.S3.AccessKey,
		SecretKey:    b.S3.SecretKey,
		StorageClass: b.S3.StorageClass,
	}
	if cfg.Enabled {
		if cfg.SecretKey == "" {
			prev, err := s.settings.GetS3(r.Context())
			if err != nil {
				Error(w, http.StatusInternalServerError, "internal", err.Error())
				return
			}
			cfg.SecretKey = prev.SecretKey
		}
		backend, err := storage.NewS3(cfg)
		if err != nil {
			Error(w, http.StatusBadRequest, "bad_s3_config", err.Error())
			return
		}
		if err := backend.Ping(r.Context()); err != nil {
			Error(w, http.StatusBadGateway, "s3_unreachable",
				"cannot reach bucket: "+err.Error())
			return
		}
	}
	if err := s.settings.SetS3(r.Context(), cfg); err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	u := auth.UserFrom(r.Context())
	audit.Log(r.Context(), s.pool, audit.Entry{
		UserID: u.ID, Action: "settings.update", IP: clientIP(r),
		Detail: map[string]any{"s3_enabled": cfg.Enabled, "bucket": cfg.Bucket},
	})
	s.getSettings(w, r)
}

// --- storage ---------------------------------------------------------------

// storageOverview aggregates per-camera/per-tier usage, disk pressure and
// retention settings for the storage admin view (docs/03-api.md).
func (s *Server) storageOverview(w http.ResponseWriter, r *http.Request) {
	stats, err := s.segs.StorageStats(r.Context())
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	cams, err := s.cams.List(r.Context(), "")
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}

	type camUsage struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		LocalBytes    int64  `json:"local_bytes"`
		S3Bytes       int64  `json:"s3_bytes"`
		SegmentCount  int    `json:"segment_count"`
		BitrateKbps   int    `json:"bitrate_kbps"`
		RetentionDays int    `json:"retention_days"`
		RetentionGB   *int   `json:"retention_gb"`
		TierAfterDays *int   `json:"tier_after_days"`
	}
	byCam := map[string]*camUsage{}
	for _, c := range cams {
		byCam[c.ID] = &camUsage{
			ID: c.ID, Name: c.Name,
			RetentionDays: c.RetentionDays, RetentionGB: c.RetentionGB,
			TierAfterDays: c.TierAfterDays,
		}
		if live := s.ingest.Stats(c.ID); live != nil {
			if kbps, ok := live["bitrate_kbps"].(int); ok {
				byCam[c.ID].BitrateKbps = kbps
			}
		}
	}
	for _, st := range stats {
		cu, ok := byCam[st.CameraID]
		if !ok {
			continue
		}
		cu.SegmentCount += st.SegmentCount
		if st.Storage == "s3" {
			cu.S3Bytes += st.Bytes
		} else {
			cu.LocalBytes += st.Bytes
		}
	}
	out := []*camUsage{}
	for _, c := range cams {
		out = append(out, byCam[c.ID])
	}

	var diskTotal, diskFree uint64
	if l, ok := s.backend.(*storage.Local); ok {
		diskTotal, diskFree = l.Usage()
	}
	s3cfg, err := s.settings.GetS3(r.Context())
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	JSON(w, http.StatusOK, map[string]any{
		"local": map[string]any{
			"total_bytes": diskTotal,
			"free_bytes":  diskFree,
		},
		"s3": map[string]any{
			"enabled": s3cfg.Enabled,
			"bucket":  s3cfg.Bucket,
		},
		"cameras": out,
	})
}
