package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagostin/tapetum/internal/audit"
	"github.com/sagostin/tapetum/internal/auth"
	"github.com/sagostin/tapetum/internal/export"
)

func (s *Server) createExport(w http.ResponseWriter, r *http.Request) {
	var b struct {
		CameraID string `json:"camera_id"`
		Start    string `json:"start"`
		End      string `json:"end"`
	}
	if err := Decode(w, r, &b); err != nil || b.CameraID == "" {
		Error(w, http.StatusBadRequest, "bad_request", "camera_id, start, end required")
		return
	}
	start, err1 := time.Parse(time.RFC3339, b.Start)
	end, err2 := time.Parse(time.RFC3339, b.End)
	if err1 != nil || err2 != nil {
		Error(w, http.StatusBadRequest, "bad_request", "start/end must be RFC 3339")
		return
	}
	u := auth.UserFrom(r.Context())
	if !s.cams.CanAccess(r.Context(), u.ID, b.CameraID) {
		Error(w, http.StatusNotFound, "camera_not_found", "camera not found")
		return
	}
	e, err := s.exporter.Enqueue(r.Context(), b.CameraID, u.ID, start, end)
	if errors.Is(err, export.ErrBusy) {
		Error(w, http.StatusConflict, "export_busy", err.Error())
		return
	}
	if err != nil {
		Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	audit.Log(r.Context(), s.pool, audit.Entry{
		UserID: u.ID, Action: "export.create", Target: e.ID, IP: clientIP(r),
		Detail: map[string]any{"camera_id": b.CameraID, "start": start, "end": end},
	})
	JSON(w, http.StatusAccepted, e)
}

func (s *Server) listExports(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	list, err := s.exporter.List(r.Context(), u.ID, u.Role == auth.RoleAdmin)
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	JSON(w, http.StatusOK, map[string]any{"exports": list})
}

func (s *Server) downloadExport(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	e, err := s.exporter.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		Error(w, http.StatusNotFound, "export_not_found", "export not found")
		return
	}
	if e.UserID != u.ID && u.Role != auth.RoleAdmin {
		Error(w, http.StatusNotFound, "export_not_found", "export not found")
		return
	}
	if e.Status != "done" {
		Error(w, http.StatusConflict, "export_not_ready", "export is "+e.Status)
		return
	}
	f, err := s.exporter.File(r.Context(), e)
	if err != nil {
		Error(w, http.StatusNotFound, "export_missing", "export file missing")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Disposition", `attachment; filename="tapetum-`+e.ID+`.mp4"`)
	http.ServeContent(w, r, "", e.CreatedAt, f)
}
