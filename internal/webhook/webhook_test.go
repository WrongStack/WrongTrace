package webhook

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// holdRequestServer answers 204 only after hold has elapsed, so a client with a
// shorter deadline must fail before any response arrives.
func holdRequestServer(t *testing.T, hold time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(hold):
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

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

// TestUpdateConfigAppliesTimeout is the regression guard for the inert-timeout
// bug: NewDispatcher baked Config.Timeout into d.httpClient, but UpdateConfig
// only assigned d.cfg, so a Timeout delivered by a runtime settings update was
// silently ignored for the life of the daemon.
func TestUpdateConfigAppliesTimeout(t *testing.T) {
	srv := holdRequestServer(t, time.Second)
	disp := NewDispatcher(Config{GenericURL: srv.URL, Timeout: 5 * time.Second})

	disp.UpdateConfig(Config{GenericURL: srv.URL, Timeout: 250 * time.Millisecond})
	if got := disp.httpClient.Timeout; got != 250*time.Millisecond {
		t.Fatalf("UpdateConfig did not change the effective HTTP timeout: got %v, want 250ms", got)
	}

	// settings.go ApplySettings rebuilds webhook.Config with no Timeout field.
	// That must resolve to the documented default, never 0 (== unlimited).
	disp.UpdateConfig(Config{GenericURL: srv.URL})
	if got := disp.httpClient.Timeout; got != defaultDispatchTimeout {
		t.Fatalf("omitted Timeout must keep the default: got %v, want %v", got, defaultDispatchTimeout)
	}

	// A negative value is treated as unset rather than as an expired deadline.
	disp.UpdateConfig(Config{GenericURL: srv.URL, Timeout: -time.Second})
	if got := disp.httpClient.Timeout; got != defaultDispatchTimeout {
		t.Fatalf("negative Timeout must keep the default: got %v, want %v", got, defaultDispatchTimeout)
	}
}

// TestUpdateConfigTimeoutHonoredBySender drives the claim through the real
// delivery path, so the guard fails on behaviour and not on a field someone
// could rename. The client (not the server) is the witness: sendGeneric
// returns the error from d.httpClient.Do.
func TestUpdateConfigTimeoutHonoredBySender(t *testing.T) {
	srv := holdRequestServer(t, 400*time.Millisecond)
	disp := NewDispatcher(Config{GenericURL: srv.URL, Timeout: 5 * time.Second})
	disp.UpdateConfig(Config{GenericURL: srv.URL, Timeout: 50 * time.Millisecond})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	err := disp.sendGeneric(ctx, srv.URL, Payload{EventType: EventSpendAlert, Message: "timeout update"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("delivery succeeded after %v: the stale construction-time 5s client is still in use", elapsed)
	}
	if !strings.Contains(err.Error(), "timeout") && !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("expected a deadline error from the updated timeout, got: %v", err)
	}
	if elapsed > 300*time.Millisecond {
		t.Fatalf("updated 50ms timeout not honoured; sender gave up only after %v", elapsed)
	}
}

// TestUpdateConfigConcurrentWithDispatchIsRaceFree pins the reason the fix
// swaps the client instead of mutating it: readers snapshot the active client
// under the lock while a writer republishes it, so no unsynchronised access to
// d.httpClient remains. Run it under -race; every update here sets the same
// value, which makes the final deadline deterministic.
func TestUpdateConfigConcurrentWithDispatchIsRaceFree(t *testing.T) {
	srv := holdRequestServer(t, 20*time.Millisecond)
	disp := NewDispatcher(Config{GenericURL: srv.URL, Timeout: time.Second})

	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			disp.Dispatch(Payload{EventType: EventThrashingAlert, Message: "concurrent"})
		}()
		go func() {
			defer wg.Done()
			disp.UpdateConfig(Config{GenericURL: srv.URL, Timeout: time.Millisecond})
		}()
	}
	wg.Wait()

	if got := disp.client().Timeout; got != time.Millisecond {
		t.Fatalf("republished timeout = %v, want %v", got, time.Millisecond)
	}
}

// drainThenHoldServer reads the whole request body, then holds until the client
// gives up (counted in cancelled) or hold elapses (counted in answered). Draining
// first is essential: an httptest handler that blocks before consuming the body
// never observes the client's deadline.
func drainThenHoldServer(t *testing.T, hold time.Duration) (*httptest.Server, *int64, *int64) {
	t.Helper()
	var cancelled, answered int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
			atomic.AddInt64(&cancelled, 1)
		case <-time.After(hold):
			atomic.AddInt64(&answered, 1)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &cancelled, &answered
}

func awaitCounter(t *testing.T, cancelled, answered *int64, bound time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(bound)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(answered) >= 1 {
			return "answered"
		}
		if atomic.LoadInt64(cancelled) >= 1 {
			return "cancelled"
		}
		time.Sleep(10 * time.Millisecond)
	}
	return "timeout"
}

// TestDeliveryTimeoutMirrorsConfiguredClient is the always-on guard: the batch
// deadline Dispatch applies must be the configured per-request timeout, never a
// fixed constant that silently truncates it.
func TestDeliveryTimeoutMirrorsConfiguredClient(t *testing.T) {
	disp := NewDispatcher(Config{Timeout: 30 * time.Second})
	if got := disp.deliveryTimeout(); got != 30*time.Second {
		t.Fatalf("deliveryTimeout() = %v, want 30s (a fixed cap here re-introduces the bug)", got)
	}

	disp.UpdateConfig(Config{Timeout: 45 * time.Second})
	if got := disp.deliveryTimeout(); got != 45*time.Second {
		t.Fatalf("deliveryTimeout() after UpdateConfig = %v, want 45s", got)
	}

	// Omitted or negative values resolve to the documented default, so the
	// surrounding context can never become 0 (== unlimited) or already-expired.
	disp.UpdateConfig(Config{})
	if got := disp.deliveryTimeout(); got != defaultDispatchTimeout {
		t.Fatalf("deliveryTimeout() with unset Timeout = %v, want %v", got, defaultDispatchTimeout)
	}
	disp.UpdateConfig(Config{Timeout: -time.Second})
	if got := disp.deliveryTimeout(); got != defaultDispatchTimeout {
		t.Fatalf("deliveryTimeout() with negative Timeout = %v, want %v", got, defaultDispatchTimeout)
	}
}

// TestDeliverSkipsEmptyTargets keeps the best-effort semantics Dispatch relied
// on: an unconfigured endpoint is a no-op and must not consume a deadline slot.
func TestDeliverSkipsEmptyTargets(t *testing.T) {
	disp := NewDispatcher(Config{Timeout: time.Second})
	var called int
	err := func() (panicked bool) {
		defer func() { panicked = recover() != nil }()
		disp.deliver(func(context.Context, string, Payload) error {
			called++
			return nil
		}, "", Payload{}, time.Second)
		return false
	}()
	if err {
		t.Fatal("deliver panicked on an empty target")
	}
	if called != 0 {
		t.Fatalf("deliver invoked the sender %d times for an empty URL, want 0", called)
	}
}

// TestDispatchHonorsTimeoutAboveTheOldTenSecondCap is the end-to-end proof that
// a delivery configured to outlive the former hardcoded 10s batch context
// completes. It must cross that boundary to be meaningful, so it is skipped
// under -short.
func TestDispatchHonorsTimeoutAboveTheOldTenSecondCap(t *testing.T) {
	if testing.Short() {
		t.Skip("crosses the former hardcoded 10s batch deadline by design")
	}
	srv, cancelled, answered := drainThenHoldServer(t, 11*time.Second)
	disp := NewDispatcher(Config{GenericURL: srv.URL, Timeout: 20 * time.Second})

	disp.Dispatch(Payload{EventType: EventSpendAlert, Severity: "critical", Message: "above old cap"})

	if got := awaitCounter(t, cancelled, answered, 18*time.Second); got != "answered" {
		t.Fatalf("delivery with Timeout=20s ended %q; the batch context must derive from Config.Timeout, "+
			"not a fixed 10s (cancelled=%d answered=%d)", got,
			atomic.LoadInt64(cancelled), atomic.LoadInt64(answered))
	}
}
