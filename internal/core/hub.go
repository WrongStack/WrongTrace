package core

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Hub fans out engine events to every connected WebSocket client. It is a
// minimal pub/sub: subscribers register, receive broadcasts until they
// disconnect, and never block the producer for slow consumers.
type Hub struct {
	mu      sync.RWMutex
	clients map[*websocket.Conn]chan WSEvent
}

// NewHub returns a fresh, empty Hub.
func NewHub() *Hub {
	return &Hub{clients: make(map[*websocket.Conn]chan WSEvent)}
}

// Subscribe registers a WebSocket connection. The returned channel delivers
// events; the caller is responsible for forwarding them to the wire and
// calling Unsubscribe when the connection closes.
func (h *Hub) Subscribe(c *websocket.Conn) <-chan WSEvent {
	ch := make(chan WSEvent, 64)
	h.mu.Lock()
	h.clients[c] = ch
	h.mu.Unlock()
	return ch
}

// Unsubscribe removes a connection and closes its delivery channel. Safe to
// call multiple times for the same connection.
func (h *Hub) Unsubscribe(c *websocket.Conn) {
	h.mu.Lock()
	if ch, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(ch)
	}
	h.mu.Unlock()
}

// Broadcast delivers ev to every subscriber, dropping it for clients whose
// buffers are full rather than blocking the engine. The dashboard's
// reconnect-on-disconnect handles the lost packets; the engine must never
// stall on a slow reader.
func (h *Hub) Broadcast(ev WSEvent) {
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if len(h.clients) > 0 {
		// Encode once here: every subscriber would otherwise marshal the
		// identical payload in its own writer goroutine. Diff snippets make
		// these frames large enough that N× encodes showed up as real GC
		// pressure with several dashboards open.
		if b, err := json.Marshal(ev); err == nil {
			ev.Wire = b
		}
	}
	for _, ch := range h.clients {
		select {
		case ch <- ev:
		default:
			// Drop; client is too slow. They will reconnect and pull /api/metrics.
		}
	}
}

// ClientCount returns the current subscriber count (for diagnostics).
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}
