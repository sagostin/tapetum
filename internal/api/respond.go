// Package api wires the HTTP API: router, middleware, handlers.
// See docs/03-api.md for the surface contract.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
)

// Error envelope: {"error": {"code": "…", "message": "…"}}.
type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// JSON writes v as JSON with the given status.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		if err := json.NewEncoder(w).Encode(v); err != nil {
			slog.Debug("json encode failed", "err", err)
		}
	}
}

// Error writes the standard error envelope.
func Error(w http.ResponseWriter, status int, code, message string) {
	var body errorBody
	body.Error.Code = code
	body.Error.Message = message
	JSON(w, status, body)
}

var errBodyTooLarge = errors.New("request body too large")

// clientIP strips the port from r.RemoteAddr (chi's RealIP middleware may
// already have replaced it with a bare IP).
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// Decode reads a JSON request body (max 1 MiB) into v.
func Decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return errBodyTooLarge
		}
		return err
	}
	return nil
}
