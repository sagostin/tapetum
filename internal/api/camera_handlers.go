package api

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagostin/tapetum/internal/audit"
	"github.com/sagostin/tapetum/internal/auth"
	"github.com/sagostin/tapetum/internal/camera"
	"github.com/sagostin/tapetum/internal/ingest"
)

// cameraBody is the create/update payload from docs/03-api.md.
type cameraBody struct {
	Name          *string        `json:"name"`
	MainURL       *string        `json:"main_url"`
	SubURL        *string        `json:"sub_url"`
	Username      *string        `json:"username"`
	Password      *string        `json:"password"`
	Transport     *string        `json:"transport"`
	RecordMode    *string        `json:"record_mode"`
	RetentionDays *int           `json:"retention_days"`
	RetentionGB   *int           `json:"retention_gb"`
	GroupID       *string        `json:"group_id"`
	MotionConfig  map[string]any `json:"motion_config"`
	AIConfig      map[string]any `json:"ai_config"`
}

func validTransport(t string) bool { return t == "tcp" || t == "udp" || t == "auto" }
func validRecordMode(m string) bool {
	return m == "continuous" || m == "motion" || m == "off"
}

func (s *Server) listCameras(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	cams, err := s.cams.List(r.Context(), u.ID)
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	JSON(w, http.StatusOK, map[string]any{"cameras": cams})
}

func (s *Server) createCamera(w http.ResponseWriter, r *http.Request) {
	var b cameraBody
	if err := Decode(w, r, &b); err != nil {
		Error(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if b.Name == nil || *b.Name == "" || b.MainURL == nil || *b.MainURL == "" {
		Error(w, http.StatusBadRequest, "bad_request", "name and main_url are required")
		return
	}
	p := camera.CreateParams{
		Name: *b.Name, MainURL: *b.MainURL, SubURL: b.SubURL,
		Transport: "tcp", RecordMode: "continuous", RetentionDays: 14,
	}
	if b.Username != nil {
		p.Username = *b.Username
	}
	if b.Password != nil {
		p.Password = *b.Password
	}
	if b.Transport != nil {
		if !validTransport(*b.Transport) {
			Error(w, http.StatusBadRequest, "bad_request", "transport must be tcp|udp|auto")
			return
		}
		p.Transport = *b.Transport
	}
	if b.RecordMode != nil {
		if !validRecordMode(*b.RecordMode) {
			Error(w, http.StatusBadRequest, "bad_request", "record_mode must be continuous|motion|off")
			return
		}
		p.RecordMode = *b.RecordMode
	}
	if b.RetentionDays != nil {
		p.RetentionDays = *b.RetentionDays
	}
	p.RetentionGB = b.RetentionGB
	p.GroupID = b.GroupID

	cam, err := s.cams.Create(r.Context(), p)
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	u := auth.UserFrom(r.Context())
	audit.Log(r.Context(), s.pool, audit.Entry{
		UserID: u.ID, Action: "camera.create", Target: cam.ID, IP: clientIP(r),
		Detail: map[string]any{"name": cam.Name},
	})
	s.ingest.Sync(r.Context(), cam.ID)
	JSON(w, http.StatusCreated, cam)
}

// cameraFor fetches the path camera and enforces the caller's ACL.
func (s *Server) cameraFor(w http.ResponseWriter, r *http.Request) *camera.Camera {
	return s.cameraForParam(w, r, "id")
}

// cameraForParam is cameraFor with a configurable URL param name
// ("id" for /cameras/{id}, "cameraId" for /playback|streams routes).
func (s *Server) cameraForParam(w http.ResponseWriter, r *http.Request, param string) *camera.Camera {
	id := chi.URLParam(r, param)
	cam, err := s.cams.Get(r.Context(), id)
	if errors.Is(err, camera.ErrNotFound) {
		Error(w, http.StatusNotFound, "camera_not_found", "camera not found")
		return nil
	}
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return nil
	}
	u := auth.UserFrom(r.Context())
	if !s.cams.CanAccess(r.Context(), u.ID, cam.ID) {
		Error(w, http.StatusNotFound, "camera_not_found", "camera not found")
		return nil
	}
	return cam
}

func (s *Server) getCamera(w http.ResponseWriter, r *http.Request) {
	cam := s.cameraFor(w, r)
	if cam == nil {
		return
	}
	JSON(w, http.StatusOK, cam)
}

func (s *Server) updateCamera(w http.ResponseWriter, r *http.Request) {
	cam := s.cameraFor(w, r)
	if cam == nil {
		return
	}
	var b cameraBody
	if err := Decode(w, r, &b); err != nil {
		Error(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if b.Transport != nil && !validTransport(*b.Transport) {
		Error(w, http.StatusBadRequest, "bad_request", "transport must be tcp|udp|auto")
		return
	}
	if b.RecordMode != nil && !validRecordMode(*b.RecordMode) {
		Error(w, http.StatusBadRequest, "bad_request", "record_mode must be continuous|motion|off")
		return
	}
	updated, err := s.cams.Update(r.Context(), cam.ID, camera.UpdateParams{
		Name: b.Name, MainURL: b.MainURL, SubURL: b.SubURL, Username: b.Username,
		Password: b.Password, Transport: b.Transport, RecordMode: b.RecordMode,
		RetentionDays: b.RetentionDays, RetentionGB: b.RetentionGB,
		GroupID: b.GroupID, MotionConfig: b.MotionConfig, AIConfig: b.AIConfig,
	})
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	u := auth.UserFrom(r.Context())
	audit.Log(r.Context(), s.pool, audit.Entry{
		UserID: u.ID, Action: "camera.update", Target: cam.ID, IP: clientIP(r),
	})
	s.ingest.Sync(r.Context(), cam.ID)
	JSON(w, http.StatusOK, updated)
}

func (s *Server) deleteCamera(w http.ResponseWriter, r *http.Request) {
	cam := s.cameraFor(w, r)
	if cam == nil {
		return
	}
	s.ingest.Remove(cam.ID)

	if r.URL.Query().Get("delete_recordings") == "true" {
		segs, err := s.segs.DeleteAllForCamera(r.Context(), cam.ID)
		if err == nil {
			for _, seg := range segs {
				if seg.Storage == "local" {
					_ = s.backend.Delete(r.Context(), seg.Path)
				}
			}
		}
	}
	if err := s.cams.Delete(r.Context(), cam.ID); err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	u := auth.UserFrom(r.Context())
	audit.Log(r.Context(), s.pool, audit.Entry{
		UserID: u.ID, Action: "camera.delete", Target: cam.ID, IP: clientIP(r),
		Detail: map[string]any{"name": cam.Name},
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setCameraEnabled(enabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cam := s.cameraFor(w, r)
		if cam == nil {
			return
		}
		if err := s.cams.SetEnabled(r.Context(), cam.ID, enabled); err != nil {
			Error(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		u := auth.UserFrom(r.Context())
		action := "camera.disable"
		if enabled {
			action = "camera.enable"
		}
		audit.Log(r.Context(), s.pool, audit.Entry{
			UserID: u.ID, Action: action, Target: cam.ID, IP: clientIP(r),
		})
		s.ingest.Sync(r.Context(), cam.ID)
		JSON(w, http.StatusOK, map[string]any{"enabled": enabled})
	}
}

// probeLimiter allows 10 probes/min per user (docs/03-api.md).
type probeLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func (l *probeLimiter) allow(userID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-time.Minute)
	kept := l.hits[userID][:0]
	for _, t := range l.hits[userID] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= 10 {
		l.hits[userID] = kept
		return false
	}
	l.hits[userID] = append(kept, now)
	return true
}

func (s *Server) probeCamera(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if !s.probeLimiter.allow(u.ID) {
		Error(w, http.StatusTooManyRequests, "rate_limited", "probe limit: 10/min")
		return
	}
	var b struct {
		URL       string `json:"url"`
		Username  string `json:"username"`
		Password  string `json:"password"`
		Transport string `json:"transport"`
	}
	if err := Decode(w, r, &b); err != nil || b.URL == "" {
		Error(w, http.StatusBadRequest, "bad_request", "url is required")
		return
	}
	res := ingest.Probe(r.Context(), b.URL, b.Username, b.Password, b.Transport)
	JSON(w, http.StatusOK, res)
}

func (s *Server) cameraSnapshot(w http.ResponseWriter, r *http.Request) {
	cam := s.cameraFor(w, r)
	if cam == nil {
		return
	}
	jpg, err := s.ingest.Snapshot(r.Context(), cam.ID)
	if errors.Is(err, ingest.ErrNoSnapshot) {
		Error(w, http.StatusServiceUnavailable, "no_snapshot",
			"no frame decoded yet — camera starting or offline")
		return
	}
	if err != nil {
		Error(w, http.StatusInternalServerError, "snapshot_failed", err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(jpg)
}

func (s *Server) cameraStats(w http.ResponseWriter, r *http.Request) {
	cam := s.cameraFor(w, r)
	if cam == nil {
		return
	}
	stats := s.ingest.Stats(cam.ID)
	if n, err := s.segs.CameraBytes(r.Context(), cam.ID); err == nil {
		stats["recorded_bytes"] = n
	}
	JSON(w, http.StatusOK, stats)
}
