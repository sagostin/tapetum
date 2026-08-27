package api

import (
	"net/http"
	"time"
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
