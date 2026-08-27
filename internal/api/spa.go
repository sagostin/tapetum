package api

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/sagostin/tapetum/web"
)

// spaHandler serves the embedded Vue SPA: real files when present,
// index.html fallback for client-side routes. /api and /ws never reach here.
func (s *Server) spaHandler() http.HandlerFunc {
	dist, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(dist))

	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(dist, path); err == nil {
			files.ServeHTTP(w, r)
			return
		}
		// SPA fallback.
		r.URL.Path = "/"
		files.ServeHTTP(w, r)
	}
}
