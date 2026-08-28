package api

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sagostin/tapetum/internal/audit"
	"github.com/sagostin/tapetum/internal/auth"
	"github.com/sagostin/tapetum/internal/events"
)

// --- events feed ------------------------------------------------------------

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := events.ListFilter{
		CameraID: q.Get("camera"),
		Type:     q.Get("type"),
		Label:    q.Get("label"),
		Unacked:  q.Get("unacked") == "true" || q.Get("unacked") == "1",
	}
	if f.CameraID != "" {
		u := auth.UserFrom(r.Context())
		if !s.cams.CanAccess(r.Context(), u.ID, f.CameraID) {
			Error(w, http.StatusNotFound, "camera_not_found", "camera not found")
			return
		}
	}
	var err error
	if f.From, err = parseTimeParam(r, "from"); err != nil && q.Get("from") != "" {
		Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if f.To, err = parseTimeParam(r, "to"); err != nil && q.Get("to") != "" {
		Error(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			f.Limit = n
		}
	}
	if c := q.Get("cursor"); c != "" {
		parts := strings.SplitN(c, "|", 2)
		if len(parts) != 2 {
			Error(w, http.StatusBadRequest, "bad_request", "invalid cursor")
			return
		}
		ts, err := time.Parse(time.RFC3339Nano, parts[0])
		if err != nil {
			Error(w, http.StatusBadRequest, "bad_request", "invalid cursor")
			return
		}
		f.CursorTs, f.CursorID = ts, parts[1]
	}

	list, cursor, err := s.evStore.List(r.Context(), f)
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	for _, e := range list {
		decorateEvent(e)
	}
	JSON(w, http.StatusOK, map[string]any{"events": list, "cursor": cursor})
}

// decorateEvent adds derived URLs for the feed.
func decorateEvent(e *events.Event) {
	if e.SnapshotPath != nil && *e.SnapshotPath != "" {
		if e.Metadata == nil {
			e.Metadata = map[string]any{}
		}
		e.Metadata["snapshot_url"] = "/api/v1/events/" + e.ID + "/snapshot.jpg"
	}
}

func (s *Server) getEvent(w http.ResponseWriter, r *http.Request) {
	e := s.eventForParam(w, r)
	if e == nil {
		return
	}
	decorateEvent(e)
	clip := map[string]any{}
	if e.ClipStart != nil && e.ClipEnd != nil {
		clip["start"] = e.ClipStart
		clip["end"] = e.ClipEnd
		clip["playlist"] = "/api/v1/events/" + e.ID + "/clip.m3u8"
	}
	JSON(w, http.StatusOK, map[string]any{"event": e, "clip": clip})
}

// eventForParam loads an event, enforcing camera ACL.
func (s *Server) eventForParam(w http.ResponseWriter, r *http.Request) *events.Event {
	e, err := s.evStore.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return nil
	}
	if e == nil {
		Error(w, http.StatusNotFound, "not_found", "event not found")
		return nil
	}
	u := auth.UserFrom(r.Context())
	if !s.cams.CanAccess(r.Context(), u.ID, e.CameraID) {
		Error(w, http.StatusNotFound, "not_found", "event not found")
		return nil
	}
	return e
}

func (s *Server) eventSnapshot(w http.ResponseWriter, r *http.Request) {
	e := s.eventForParam(w, r)
	if e == nil {
		return
	}
	if e.SnapshotPath == nil || *e.SnapshotPath == "" {
		Error(w, http.StatusNotFound, "no_snapshot", "event has no snapshot")
		return
	}
	rc, err := s.backend.Open(r.Context(), *e.SnapshotPath)
	if err != nil {
		Error(w, http.StatusNotFound, "no_snapshot", "snapshot file missing")
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = io.Copy(w, rc)
}

// eventClip builds an on-demand HLS playlist over the event's clip range.
func (s *Server) eventClip(w http.ResponseWriter, r *http.Request) {
	e := s.eventForParam(w, r)
	if e == nil {
		return
	}
	if e.ClipStart == nil || e.ClipEnd == nil {
		Error(w, http.StatusNotFound, "no_clip", "event has no clip range yet")
		return
	}
	segs, err := s.segs.SegmentsInRange(r.Context(), e.CameraID, *e.ClipStart, *e.ClipEnd)
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if len(segs) == 0 {
		Error(w, http.StatusNotFound, "no_recordings", "no recordings for clip range")
		return
	}
	servePlaylist(w, buildPlaylist(e.CameraID, segs, false, wantsTranscode(r), *e.ClipStart, *e.ClipEnd))
}

func (s *Server) ackEvent(w http.ResponseWriter, r *http.Request) {
	e := s.eventForParam(w, r)
	if e == nil {
		return
	}
	u := auth.UserFrom(r.Context())
	if err := s.evStore.Ack(r.Context(), e.ID, u.ID); err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	JSON(w, http.StatusOK, map[string]any{"acked": true})
}

func (s *Server) deleteEvent(w http.ResponseWriter, r *http.Request) {
	e := s.eventForParam(w, r)
	if e == nil {
		return
	}
	deleted, err := s.evStore.Delete(r.Context(), e.ID)
	if err != nil {
		Error(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if deleted.SnapshotPath != nil && *deleted.SnapshotPath != "" {
		_ = s.backend.Delete(r.Context(), *deleted.SnapshotPath)
	}
	u := auth.UserFrom(r.Context())
	audit.Log(r.Context(), s.pool, audit.Entry{
		UserID: u.ID, Action: "event.delete", Target: e.ID, IP: clientIP(r),
		Detail: map[string]any{"camera_id": e.CameraID},
	})
	w.WriteHeader(http.StatusNoContent)
}
