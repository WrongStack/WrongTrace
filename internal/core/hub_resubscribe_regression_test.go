package core

import (
	"reflect"
	"testing"

	"github.com/gorilla/websocket"
)

type hubChannelObservation struct {
	ids    []string
	closed bool
}

func observeHubChannel(ch <-chan WSEvent) hubChannelObservation {
	var o hubChannelObservation
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				o.closed = true
				return o
			}
			o.ids = append(o.ids, ev.EventID)
		default:
			return o
		}
	}
}

// Duplicate registration must not abandon the channel held by the writer.
// The gate forces that original observer to finish after disconnect.
func TestHub_ResubscribeRetainsChannelOwnership(t *testing.T) {
	h := NewHub()
	c := new(websocket.Conn) // Hub uses the connection only as an opaque identity.
	old := h.Subscribe(c)
	h.Broadcast(WSEvent{EventID: "before"})
	gate := make(chan struct{})
	result := make(chan hubChannelObservation, 1)
	go func() { <-gate; result <- observeHubChannel(old) }()
	again := h.Subscribe(c)
	h.Broadcast(WSEvent{EventID: "after"})
	h.Unsubscribe(c)
	close(gate)
	o := <-result
	if !o.closed || !reflect.DeepEqual(o.ids, []string{"before", "after"}) {
		t.Fatalf("original writer channel after disconnect: %+v; want both ordered events and closed", o)
	}
	if old != again || h.ClientCount() != 0 {
		t.Fatal("duplicate registration lost channel identity or disconnect left a client")
	}
	h.Unsubscribe(c)
	h.Broadcast(WSEvent{EventID: "ghost"})
	fresh := h.Subscribe(c)
	if fresh == old {
		t.Fatal("restart reused closed channel")
	}
	h.Broadcast(WSEvent{EventID: "restart"})
	f := observeHubChannel(fresh)
	if f.closed || !reflect.DeepEqual(f.ids, []string{"restart"}) || !observeHubChannel(old).closed {
		t.Fatalf("restart leaked into old subscription or missed live subscriber: %+v", f)
	}
	h.Unsubscribe(c)
	if !observeHubChannel(fresh).closed {
		t.Fatal("restart channel not closed")
	}
}

func TestHub_ConcurrentResubscribeSharesOneChannel(t *testing.T) {
	h := NewHub()
	c := new(websocket.Conn)
	start := make(chan struct{})
	channels := make(chan (<-chan WSEvent), 8)
	for i := 0; i < 8; i++ {
		go func() { <-start; channels <- h.Subscribe(c) }()
	}
	close(start)
	first := <-channels
	for i := 1; i < 8; i++ {
		if <-channels != first {
			t.Fatal("concurrent subscriber was orphaned")
		}
	}
	if h.ClientCount() != 1 {
		t.Fatal("duplicate registrations increased client count")
	}
	h.Unsubscribe(c)
	if !observeHubChannel(first).closed {
		t.Fatal("shared delivery channel not closed")
	}
}
