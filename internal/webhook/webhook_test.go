package webhook

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestDispatcher_AllChannelsAndErrors(t *testing.T) {
	var mu sync.Mutex
	var receivedSlack []map[string]interface{}
	var receivedDiscord []map[string]interface{}
	var receivedGeneric []Payload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		switch r.URL.Path {
		case "/slack":
			var m map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&m)
			receivedSlack = append(receivedSlack, m)
			w.WriteHeader(http.StatusOK)
		case "/discord":
			var m map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&m)
			receivedDiscord = append(receivedDiscord, m)
			w.WriteHeader(http.StatusOK)
		case "/generic":
			var p Payload
			_ = json.NewDecoder(r.Body).Decode(&p)
			receivedGeneric = append(receivedGeneric, p)
			w.WriteHeader(http.StatusOK)
		case "/error":
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	disp := NewDispatcher(Config{
		SlackURL:   srv.URL + "/slack",
		DiscordURL: srv.URL + "/discord",
		GenericURL: srv.URL + "/generic",
		Timeout:    2 * time.Second,
	})

	// 1. Dispatch info alert
	disp.Dispatch(Payload{
		EventType: EventThrashingAlert,
		Severity:  "info",
		Message:   "Info test",
		Details:   map[string]interface{}{"foo": "bar"},
	})

	// 2. Dispatch warning alert
	disp.Dispatch(Payload{
		EventType: EventSpendAlert,
		Severity:  "warning",
		Message:   "Spend warning",
	})

	// 3. Dispatch critical alert
	disp.Dispatch(Payload{
		EventType: EventGuardrailBlock,
		Severity:  "critical",
		Message:   "Guardrail blocked file edit",
	})

	// 4. UpdateConfig to error endpoint & empty
	disp.UpdateConfig(Config{
		SlackURL:   srv.URL + "/error",
		DiscordURL: srv.URL + "/error",
		GenericURL: srv.URL + "/error",
	})
	disp.Dispatch(Payload{
		EventType: EventSelfRollback,
		Severity:  "unknown",
		Message:   "Error endpoint test",
	})

	disp.UpdateConfig(Config{})
	disp.Dispatch(Payload{
		EventType: EventSelfRollback,
		Severity:  "info",
		Message:   "Empty config test",
	})

	// Wait for background goroutines
	time.Sleep(150 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	if len(receivedSlack) < 3 {
		t.Errorf("expected at least 3 slack payloads, got %d", len(receivedSlack))
	}
	if len(receivedDiscord) < 3 {
		t.Errorf("expected at least 3 discord payloads, got %d", len(receivedDiscord))
	}
	if len(receivedGeneric) < 3 {
		t.Errorf("expected at least 3 generic payloads, got %d", len(receivedGeneric))
	}
}

func TestDispatcherBoundsConcurrentDeliveries(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	defer close(release)

	disp := NewDispatcher(Config{GenericURL: srv.URL, Timeout: time.Second})
	for i := 0; i < maxConcurrentDeliveries*4; i++ {
		disp.Dispatch(Payload{EventType: EventGuardrailBlock})
	}

	deadline := time.Now().Add(time.Second)
	for len(disp.inFlight) < maxConcurrentDeliveries && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := len(disp.inFlight); got != maxConcurrentDeliveries {
		t.Fatalf("in-flight deliveries = %d, want bounded capacity %d", got, maxConcurrentDeliveries)
	}
}
