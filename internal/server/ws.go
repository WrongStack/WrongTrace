package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// upgrader is shared across all /api/ws connections. checkOrigin admits
// loopback origins (embedded dashboard, vite dev proxy) and same-host
// requests; remote origins are rejected because the event stream carries
// prompt/reply telemetry and the API has no authentication.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     checkOrigin,
}

func checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // non-browser clients do not send Origin
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// writeWait bounds how long a single write may block before we declare the
// client unhealthy and tear down the connection.
const (
	writeWait  = 5 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10
)

// WebSocket handles GET /api/ws. It upgrades the connection, registers it with
// the engine hub, and pumps events from the hub to the wire. The read side
// exists purely to detect client disconnects (we never expect inbound traffic
// besides the protocol-level pongs).
func (h *Handlers) WebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws: upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	conn.SetReadLimit(8192)
	if err := conn.SetReadDeadline(time.Now().Add(pongWait)); err != nil {
		log.Printf("ws: read deadline: %v", err)
		return
	}
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	events := h.Engine.Hub().Subscribe(conn)
	defer h.Engine.Hub().Unsubscribe(conn)

	// Greeting: send a hello so the client knows the connection is live.
	// Bounded like every later write — a stalled client must not pin this
	// goroutine (and its hub subscription) forever.
	if err := writeJSONWithDeadline(conn, map[string]interface{}{
		"type": "hello",
		"at":   time.Now().UTC(),
		"repo": h.Engine.Repo(),
	}); err != nil {
		return
	}

	// Reader goroutine: drains the connection so the writer's SetReadDeadline
	// gets refreshed by Pong frames, and surfaces client disconnects.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		defer cancel()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	pingTicker := time.NewTicker(pingPeriod)
	defer pingTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			if err := writeJSONWithDeadline(conn, ev); err != nil {
				return
			}
		case <-pingTicker.C:
			if err := conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
				return
			}
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// writeJSONWithDeadline serializes v to a JSON envelope and writes it with a
// bounded deadline. Errors are returned to the caller for connection teardown.
func writeJSONWithDeadline(conn *websocket.Conn, v interface{}) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, payload)
}
