// Package ws is the WebSocket hub for live UI updates (camera status,
// events, exports, storage warnings). Phase 0: connection lifecycle +
// broadcast skeleton; topics get wired to real events in later phases.
package ws

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
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

// Hub tracks connected clients and broadcasts messages.
type Hub struct {
	mu      sync.Mutex
	clients map[*client]struct{}
}

func NewHub() *Hub {
	return &Hub{clients: make(map[*client]struct{})}
}

// ServeHTTP upgrades the request and runs the client until disconnect.
// Auth middleware must run before this; unauthenticated requests get 401.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if u == nil {
		http.Error(w, `{"error":{"code":"unauthorized","message":"authentication required"}}`, http.StatusUnauthorized)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// SPA is same-origin in prod; dev uses vite proxy (same-origin too).
		OriginPatterns: []string{},
	})
	if err != nil {
		slog.Debug("ws accept failed", "err", err)
		return
	}

	c := &client{userID: u.ID, conn: conn, send: make(chan []byte, 64)}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	slog.Debug("ws client connected", "user", u.Username)
	defer func() {
		h.mu.Lock()
		delete(h.clients, c)
		h.mu.Unlock()
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
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c.send <- payload:
		default: // client buffer full — drop
		}
	}
}

// Count returns connected clients (for /system/stats).
func (h *Hub) Count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}
