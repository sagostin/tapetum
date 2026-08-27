package api

import (
	"errors"
	"net/http"

	"github.com/sagostin/tapetum/internal/webrtc"
)

// webrtcOffer is the WebRTC signaling route (docs/03-api.md): the client
// POSTs an SDP offer and gets back a complete answer (candidates gathered
// server-side — no trickle).
func (s *Server) webrtcOffer(w http.ResponseWriter, r *http.Request) {
	cam := s.cameraForParam(w, r, "cameraId")
	if cam == nil {
		return
	}
	var b struct {
		SDP    string `json:"sdp"`
		Stream string `json:"stream"` // "sub" (default) or "main"
	}
	if err := Decode(w, r, &b); err != nil || b.SDP == "" {
		Error(w, http.StatusBadRequest, "bad_request", "sdp offer is required")
		return
	}
	answer, err := s.webrtc.HandleOffer(r.Context(), cam.ID, b.Stream, b.SDP)
	switch {
	case errors.Is(err, webrtc.ErrUnsupportedCodec):
		Error(w, http.StatusConflict, "unsupported_codec", err.Error())
	case errors.Is(err, webrtc.ErrStreamUnavailable):
		Error(w, http.StatusServiceUnavailable, "stream_unavailable", err.Error())
	case errors.Is(err, webrtc.ErrTooManyPeers):
		Error(w, http.StatusTooManyRequests, "too_many_peers", err.Error())
	case err != nil:
		Error(w, http.StatusInternalServerError, "webrtc_failed", err.Error())
	default:
		JSON(w, http.StatusOK, map[string]any{"sdp": answer})
	}
}
