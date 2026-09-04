package server

import (
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/wrongstack/wrongtrace/internal/core"
	"github.com/wrongstack/wrongtrace/internal/db"
)

// TestRouter_DoesNotTrustForwardedClientIP guards the removal of
// chi middleware.RealIP (GHSA-3fxj-6jh8-hvhx). RealIP overwrites
// r.RemoteAddr with whatever the caller puts in X-Forwarded-For /
// X-Real-IP / True-Client-IP. Nothing terminates in front of this daemon to
// strip those headers, so with RealIP installed any client could forge the IP
// that requestLogger records and that the gateway proxy stamps onto telemetry.
// The handler must see the real socket peer instead.
func TestRouter_DoesNotTrustForwardedClientIP(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "hardening.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	engine := core.NewEngine(core.Config{RepoName: "hardening-test", Store: store})

	var seen string
	// The production router, with one extra probe route mounted on it: the
	// middleware chain under test is the real one.
	r := New(Config{Port: 0, Engine: engine}).router
	r.Get("/api/echo-peer", func(w http.ResponseWriter, req *http.Request) {
		seen = req.RemoteAddr
		w.WriteHeader(http.StatusOK)
	})

	ts := httptest.NewServer(r)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/echo-peer", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for _, h := range []string{"X-Forwarded-For", "X-Real-IP", "True-Client-IP"} {
		req.Header.Set(h, "203.0.113.7")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()

	host, _, err := net.SplitHostPort(seen)
	if err != nil {
		host = seen
	}
	if host == "203.0.113.7" {
		t.Fatalf("RemoteAddr = %q: a client-supplied forwarding header rewrote the peer "+
			"address; middleware.RealIP (or an equivalent) is back in the chain", seen)
	}
	if !net.ParseIP(host).IsLoopback() {
		t.Fatalf("RemoteAddr = %q, want the real loopback socket peer", seen)
	}
}

// TestProfilerEndpoints_ClampClientLimit guards the unbounded page size on the
// profiler endpoints. /api/profiler/hotspots aggregates over the whole
// runtime_traces table and serializes every row, so an unclamped ?limit let a
// single request pull the entire table into memory on every poll. The recent-
// events endpoints were already clamped at maxRecentEventsLimit; these two were
// missed. The clamp must hold at the handler AND at the SQL boundary.
func TestProfilerEndpoints_ClampClientLimit(t *testing.T) {
	_, _, ts := newTestServer(t)

	for _, path := range []string{
		"/api/profiler/traces?limit=100000000",
		"/api/profiler/hotspots?limit=100000000",
		"/api/metrics/friction?limit=100000000",
	} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 (an absurd limit must be clamped, not rejected "+
				"or fatal)", path, resp.StatusCode)
		}
	}
}

// TestProfilerHotspots_StoreClampsLimit pins the clamp at the layer that
// actually runs the query, so a future caller that bypasses the HTTP handler
// still cannot ask for an unbounded result set.
func TestProfilerHotspots_StoreClampsLimit(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "hotspots.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := store.ProfilerHotspots(1 << 30); err != nil {
		t.Fatalf("ProfilerHotspots with an absurd limit: %v", err)
	}
}

// TestHealth_CarriesServiceMarker pins the contract that internal/lock relies
// on for single-instance detection. The startup probe treats a port as taken
// by a WrongTrace daemon only when GET /api/health identifies itself; if this
// marker is renamed or dropped, a genuinely running daemon stops being
// recognized and the operator gets a raw bind error instead of the friendly
// "already running" message. The two packages cannot import each other, so
// this test is the seam that keeps them in sync.
func TestHealth_CarriesServiceMarker(t *testing.T) {
	_, _, ts := newTestServer(t)

	var payload struct {
		Service string `json:"service"`
		OK      bool   `json:"ok"`
	}
	resp := getJSON(t, ts.URL+"/api/health", &payload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/api/health = %d, want 200", resp.StatusCode)
	}
	// Must equal lock.healthServiceMarker, which is unexported; the literal is
	// duplicated here deliberately so a rename on either side fails loudly.
	if payload.Service != "wrongtrace" {
		t.Errorf("health service marker = %q, want %q (internal/lock's isPortActive "+
			"matches on this exact value)", payload.Service, "wrongtrace")
	}
	if !payload.OK {
		t.Errorf("health ok = false, want true")
	}
}
