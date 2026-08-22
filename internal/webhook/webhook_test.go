package webhook

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestDispatcher(t *testing.T) {
	var mu sync.Mutex
	var received []Payload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p Payload
		if err := json.NewDecoder(r.Body).Decode(&p); err == nil {
			mu.Lock()
			received = append(received, p)
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	disp := NewDispatcher(Config{
		GenericURL: srv.URL,
		Timeout:    2 * time.Second,
	})

	disp.Dispatch(Payload{
		EventType: EventThrashingAlert,
		Severity:  "critical",
		Message:   "Function CalculateTax thrashing detected (5 edits in 1 hour)",
	})

	// Wait for background dispatch
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	count := len(received)
	mu.Unlock()

	if count != 1 {
		t.Fatalf("expected 1 webhook received, got %d", count)
	}

	mu.Lock()
	item := received[0]
	mu.Unlock()

	if item.EventType != EventThrashingAlert || item.Severity != "critical" {
		t.Errorf("unexpected payload: %+v", item)
	}
}
