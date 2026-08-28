// Package ws is the WebSocket hub for live UI updates (camera status,
// events, exports, storage warnings). Phase 0: connection lifecycle +
// broadcast skeleton; topics get wired to real events in later phases.
package ws

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/sagostin/tapetum/internal/auth"
)

// Message is the WS envelope: {"topic": "…", "data": {…}}.
type Message struct {
	Topic string `json:"topic"`
	Data  any    `json:"data"`
}

type client struct {
	userID string
	conn   *websocket.Conn
	send   chan []byte
}

// Hub tracks connected clients and broadcasts messages. Clients are stored
// in an atomic.Pointer to a map so Broadcast and ServeHTTP add/remove can run
// without serializing on a shared mutex; Broadcast copies the map under a
// pointer swap and then fans out without holding any hub-wide lock.
type Hub struct {
	clients        atomic.Pointer[map[*client]struct{}]
	dev            bool // dev mode allows the Vite WS proxy origin (5173)
	insecureOrigin bool // InsecureSkipVerify for trusted dev/local setups
}

func NewHub() *Hub {
	h := &Hub{}
	empty := map[*client]struct{}{}
	h.clients.Store(&empty)
	return h
}

// SetDev toggles dev-mode origin handling. When true, the WS accept allows
// any localhost/127.0.0.1 Origin so the Vite dev server (:5173) can proxy
// WS upgrades to the backend (:8080) without a same-host mismatch.
func (h *Hub) SetDev(dev bool) {
	h.dev = dev
}

func (h *Hub) snapshot() map[*client]struct{} {
	return *h.clients.Load()
}

// acceptOptions builds WebSocket accept options. In dev mode we accept
// cross-origin WS upgrades from the Vite dev server (typically
// http://localhost:5173). In production the SPA is same-origin so the
// Origin must match the Host — that's what coder/websocket enforces when
// OriginPatterns is empty.
func (h *Hub) acceptOptions(r *http.Request) *websocket.AcceptOptions {
	if !h.dev {
		return &websocket.AcceptOptions{}
	}
	return &websocket.AcceptOptions{
		OriginPatterns: []string{
			`^https?://(localhost|127\.0\.0\.1)(:\d+)?$`,
		},
	}
}

func (h *Hub) add(c *client) {
	for {
		old := h.snapshot()
		next := make(map[*client]struct{}, len(old)+1)
		for k := range old {
			next[k] = struct{}{}
		}
		next[c] = struct{}{}
		if h.clients.CompareAndSwap(&old, &next) {
			return
		}
	}
}

func (h *Hub) drop(c *client) {
	for {
		old := h.snapshot()
		if _, ok := old[c]; !ok {
			return
		}
		next := make(map[*client]struct{}, len(old)-1)
		for k := range old {
			if k != c {
				next[k] = struct{}{}
			}
		}
		if h.clients.CompareAndSwap(&old, &next) {
			return
		}
	}
}

// ServeHTTP upgrades the request and runs the client until disconnect.
// Auth middleware must run before this; unauthenticated requests get 401.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if u == nil {
		http.Error(w, `{"error":{"code":"unauthorized","message":"authentication required"}}`, http.StatusUnauthorized)
		return
	}

	conn, err := websocket.Accept(w, r, h.acceptOptions(r))
	if err != nil {
		slog.Debug("ws accept failed", "err", err)
		return
	}
	if err != nil {
		slog.Debug("ws accept failed", "err", err)
		return
	}

	c := &client{userID: u.ID, conn: conn, send: make(chan []byte, 64)}
	h.add(c)
	slog.Debug("ws client connected", "user", u.Username)
	defer func() {
		h.drop(c)
		close(c.send)
		conn.Close(websocket.StatusNormalClosure, "bye")
	}()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go h.writePump(ctx, c)
	h.readPump(ctx, c) // blocks until disconnect
}

func (h *Hub) readPump(ctx context.Context, c *client) {
	for {
		// Clients don't send application messages yet; reads keep the
		// connection alive and detect closure. Ping frames are handled
		// by the websocket library's keepalive.
		_, _, err := c.conn.Read(ctx)
		if err != nil {
			return
		}
	}
}

func (h *Hub) writePump(ctx context.Context, c *client) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-c.send:
			if !ok {
				return
			}
			wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := c.conn.Write(wctx, websocket.MessageText, msg)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

// Broadcast sends a message to every connected client (best-effort;
// slow clients drop messages rather than blocking the hub).
func (h *Hub) Broadcast(topic string, data any) {
	payload, err := json.Marshal(Message{Topic: topic, Data: data})
	if err != nil {
		return
	}
	for c := range h.snapshot() {
		select {
		case c.send <- payload:
		default: // client buffer full — drop
		}
	}
}

// Count returns connected clients (for /system/stats).
func (h *Hub) Count() int {
	return len(h.snapshot())
}
