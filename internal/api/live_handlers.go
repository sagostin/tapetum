package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/sagostin/tapetum/internal/auth"
	"github.com/sagostin/tapetum/internal/live"
)

// liveMP4 streams a camera's main-stream access units as a continuous
// fragmented MP4 byte stream (UniFi Protect-style). The browser consumes
// it via MSE — SourceBuffer.appendBuffer per chunk, no segment boundaries
// mid-stream, ~100 ms glass-to-glass latency.
//
// ?stream=sub|main defaults to main (sub stream isn't recorded live anyway).
// H.265 cameras aren't supported here yet — the recorder's H.265 main
// segments can still be served via the HLS endpoint with ?transcode=h264.
func (s *Server) liveMP4(w http.ResponseWriter, r *http.Request) {
	camID := chi.URLParam(r, "cameraId")
	u := auth.UserFrom(r.Context())
	if !s.cams.CanAccess(r.Context(), u.ID, camID) {
		Error(w, http.StatusNotFound, "camera_not_found", "camera not found")
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		Error(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}

	sub := r.URL.Query().Get("stream") == "sub"

	codec, sps, pps, vps, ch, cancel, ok := s.liveHub.Subscribe(camID, sub)
	if !ok && sub {
		// Cameras without a sub stream: fall back to main when it's H.264.
		sub = false
		codec, sps, pps, vps, ch, cancel, ok = s.liveHub.Subscribe(camID, sub)
	}
	if !ok {
		Error(w, http.StatusNotFound, "stream_unavailable",
			"no live stream available for this camera")
		return
	}
	defer cancel()
	if codec != "h264" {
		Error(w, http.StatusConflict, "unsupported_codec",
			"live fMP4 requires H.264 — use the HLS endpoint with ?transcode=h264 for H.265")
		return
	}

	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	muxer := live.NewMuxer(codec, sps, pps, vps)
	if err := muxer.WriteInit(w); err != nil {
		slog.With("component", "live").Debug("live.mp4 write init", "err", err)
		return
	}
	fl.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case f, ok := <-ch:
			if !ok {
				return
			}
			if err := muxer.WriteSample(w, f.AU, f.PTS, f.Keyframe); err != nil {
				if !errors.Is(err, http.ErrHandlerTimeout) {
					slog.With("component", "live").Debug("live.mp4 write sample", "err", err)
				}
				return
			}
			fl.Flush()
		}
	}
}
