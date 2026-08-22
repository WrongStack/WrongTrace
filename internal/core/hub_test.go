package core

import (
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// dialWS performs a real loopback WebSocket handshake and returns the
// server-side conn. The Hub treats the *websocket.Conn as an opaque
// subscriber handle, so the test never reads from the wire — the handshake
// just guarantees a genuine, distinct conn per subscriber.
func dialWS(t *testing.T) *websocket.Conn {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	accepted := make(chan *websocket.Conn, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		u := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		c, err := u.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		accepted <- c
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })

	d := websocket.Dialer{HandshakeTimeout: 2 * time.Second}
	client, _, err := d.Dial("ws://"+l.Addr().String()+"/", nil)
	if err != nil {
		t.Fatalf("dial %s: %v", l.Addr(), err)
	}
	t.Cleanup(func() { _ = client.Close() })

	select {
	case c := <-accepted:
		t.Cleanup(func() { _ = c.Close() })
		return c
	case <-time.After(2 * time.Second):
		t.Fatal("server never upgraded the websocket connection")
	}
	return nil
}

// recvOne waits for a single event with a hard deadline.
func recvOne(t *testing.T, ch <-chan WSEvent) WSEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("subscriber channel closed unexpectedly")
		}
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for broadcast delivery")
	}
	return WSEvent{}
}

// drain collects events until 100ms pass with nothing arriving.
func drain(ch <-chan WSEvent) []WSEvent {
	var out []WSEvent
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-time.After(100 * time.Millisecond):
			return out
		}
	}
}

func TestHub_FanOutToAllSubscribers(t *testing.T) {
	h := NewHub()
	subs := []<-chan WSEvent{
		h.Subscribe(dialWS(t)),
		h.Subscribe(dialWS(t)),
		h.Subscribe(dialWS(t)),
	}
	if n := h.ClientCount(); n != 3 {
		t.Fatalf("ClientCount = %d, want 3", n)
	}

	h.Broadcast(WSEvent{Type: "code_event", Payload: "p", EventID: "e1"})

	for i, ch := range subs {
		ev := recvOne(t, ch)
		if ev.Type != "code_event" || ev.EventID != "e1" {
			t.Errorf("subscriber %d got %+v, want code_event/e1", i, ev)
		}
		if ev.At.IsZero() {
			t.Errorf("subscriber %d: Broadcast must stamp At when zero", i)
		}
	}
}

// TestHub_SlowConsumerDroppedNotBlocking pins the core liveness contract:
// a subscriber that stops reading must never stall Broadcast. The slow
// subscriber keeps exactly its buffer (the oldest events), everything newer
// is dropped, and a consumer that is provably live keeps up with the full
// burst in order.
//
// The fast reader must be genuinely fast and provably running before the
// burst: the warmup handshake proves liveness, and the lock-free
// preallocated-slot bookkeeping ensures the test harness itself cannot be
// what makes the consumer fall behind (a mutex-guarded append is slower
// than the producer and would legitimately overflow its own buffer).
func TestHub_SlowConsumerDroppedNotBlocking(t *testing.T) {
	h := NewHub()
	slowConn := dialWS(t)
	fastConn := dialWS(t)
	slow := h.Subscribe(slowConn)
	fast := h.Subscribe(fastConn)

	const burst = 200
	slots := make([]string, burst)
	var got atomic.Int64
	warm := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		i := 0
		for ev := range fast {
			if ev.EventID == "warmup" {
				close(warm) // received: reader is provably live
				continue
			}
			if i >= burst {
				continue // post-burst traffic (e.g. the "after" ping) is not counted
			}
			slots[i] = ev.EventID
			i++
			got.Add(1)
		}
	}()

	// Handshake: broadcast one warmup and wait until the reader has taken
	// it, so the burst cannot start before the consumer is scheduled.
	h.Broadcast(WSEvent{Type: "code_event", EventID: "warmup"})
	select {
	case <-warm:
	case <-time.After(2 * time.Second):
		t.Fatal("fast reader never became live before the burst")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < burst; i++ {
			h.Broadcast(WSEvent{Type: "code_event", EventID: fmt.Sprintf("e-%03d", i)})
			// Pace the producer: Broadcast uses non-blocking sends, so a
			// consumer that falls >buffer behind legitimately loses events
			// by design. A tiny sleep keeps the live reader able to keep
			// pace; the stalled subscriber still overflows regardless
			// because it never reads during the burst.
			time.Sleep(200 * time.Microsecond)
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Broadcast blocked on a slow consumer")
	}

	// Fast subscriber: every event, in order.
	complete := false
	for i := 0; i < 1000 && !complete; i++ {
		complete = got.Load() == burst
		if !complete {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if n := got.Load(); n != burst {
		t.Fatalf("fast subscriber received %d/%d events", n, burst)
	}
	for i, id := range slots {
		if id != fmt.Sprintf("e-%03d", i) {
			t.Fatalf("fast subscriber order broken at %d: got %q, want e-%03d", i, id, i)
		}
	}

	// Slow subscriber: exactly its buffer, oldest events retained (warmup
	// first), the overflow dropped on the floor.
	held := drain(slow)
	if len(held) != cap(slow) {
		t.Errorf("slow subscriber retained %d events, want exactly buffer cap %d", len(held), cap(slow))
	}
	if len(held) >= burst+1 {
		t.Fatalf("slow subscriber should have lost events (%d retained)", len(held))
	}
	if len(held) > 0 && held[0].EventID != "warmup" {
		t.Errorf("slow subscriber should keep the OLDEST event first, got %q", held[0].EventID)
	}
	if len(held) > 1 && held[len(held)-1].EventID != fmt.Sprintf("e-%03d", len(held)-2) {
		t.Errorf("slow subscriber retention not contiguous from the start: last=%q", held[len(held)-1].EventID)
	}

	// The slow subscriber is still live after drops, and the fast one too.
	h.Broadcast(WSEvent{Type: "ping", EventID: "after"})
	if ev := recvOne(t, slow); ev.EventID != "after" {
		t.Errorf("slow subscriber dead after drop; got %+v", ev)
	}

	// Tear down the fast subscription so the reader goroutine exits.
	h.Unsubscribe(fastConn)
	select {
	case <-readerDone:
	case <-time.After(2 * time.Second):
		t.Error("fast reader goroutine did not exit after unsubscribe")
	}
}

func TestHub_UnsubscribeClosesChannel(t *testing.T) {
	h := NewHub()
	conn := dialWS(t)
	ch := h.Subscribe(conn)
	if n := h.ClientCount(); n != 1 {
		t.Fatalf("ClientCount = %d, want 1", n)
	}

	h.Broadcast(WSEvent{Type: "code_event", EventID: "queued"})
	h.Unsubscribe(conn)
	if n := h.ClientCount(); n != 0 {
		t.Errorf("ClientCount after unsubscribe = %d, want 0", n)
	}

	// Buffered event survives the close, then the channel reports closed.
	ev, ok := <-ch
	if !ok || ev.EventID != "queued" {
		t.Errorf("buffered event lost on unsubscribe: %+v ok=%v", ev, ok)
	}
	if _, ok := <-ch; ok {
		t.Error("channel should be closed after Unsubscribe")
	}

	// Broadcast after removal must not panic or resurrect the client.
	h.Broadcast(WSEvent{Type: "code_event", EventID: "ghost"})
	if n := h.ClientCount(); n != 0 {
		t.Errorf("unsubscribed client resurrected: %d", n)
	}

	// Double unsubscribe is a safe no-op.
	h.Unsubscribe(conn)
}

func TestHub_BroadcastWithoutSubscribers(t *testing.T) {
	h := NewHub()
	h.Broadcast(WSEvent{Type: "code_event", EventID: "nobody"})
	if n := h.ClientCount(); n != 0 {
		t.Errorf("ClientCount = %d, want 0", n)
	}
}
