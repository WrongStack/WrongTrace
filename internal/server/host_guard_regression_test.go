package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wrongstack/wrongtrace/internal/core"
	"github.com/wrongstack/wrongtrace/internal/db"
)

func newGuardTestServer(t *testing.T, cfg Config) (*Server, *core.Engine) {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "guard.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	engine := core.NewEngine(core.Config{RepoName: "guard-test", Store: store})
	cfg.Engine = engine
	return New(cfg), engine
}

func serve(s *Server, method, target, host string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.Host = host
	req.RemoteAddr = "127.0.0.1:50000"
	if mutate != nil {
		mutate(req)
	}
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	return rec
}

// DNS rebinding: a page on attacker.test:3444 re-resolved to 127.0.0.1 sends
// Host and Origin that match each other, which the old same-origin shortcut
// accepted, handing the attacker the unauthenticated API. The Host must now
// name the daemon.
func TestHostGuard_RejectsRebindingHostWithoutToken(t *testing.T) {
	t.Setenv("WRONGTRACE_TOKEN", "")
	s, _ := newGuardTestServer(t, Config{})

	rec := serve(s, http.MethodGet, "/api/metrics/overview", "attacker.test:3444", func(r *http.Request) {
		r.Header.Set("Origin", "http://attacker.test:3444")
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("rebinding Host = %d, want 403", rec.Code)
	}
	if rec := serve(s, http.MethodGet, "/", "attacker.test:3444", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("rebinding Host on SPA = %d, want 403", rec.Code)
	}

	for _, host := range []string{"127.0.0.1:3444", "localhost:3444", "LOCALHOST", "[::1]:3444", "192.168.1.20:3444", ""} {
		if rec := serve(s, http.MethodGet, "/api/metrics/overview", host, nil); rec.Code != http.StatusOK {
			t.Errorf("Host %q = %d, want 200", host, rec.Code)
		}
	}
	// Same-origin browser calls from a legitimate Host still pass CORS.
	rec = serve(s, http.MethodGet, "/api/metrics/overview", "localhost:3444", func(r *http.Request) {
		r.Header.Set("Origin", "http://localhost:3444")
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("same-origin localhost = %d, want 200", rec.Code)
	}
}

// CLI agents reach the gateway with Host 127.0.0.1:3444 / localhost:3444; the
// guard must not get in their way.
func TestHostGuard_GatewayRoutesKeepWorkingOnLoopbackHosts(t *testing.T) {
	t.Setenv("WRONGTRACE_TOKEN", "")
	s, _ := newGuardTestServer(t, Config{})
	for _, target := range []string{"/v1/models", "/proxy/api.openai.com/v1/models"} {
		for _, host := range []string{"127.0.0.1:3444", "localhost:3444"} {
			if rec := serve(s, http.MethodGet, target, host, nil); rec.Code == http.StatusForbidden {
				t.Errorf("%s with Host %s = 403 (%s), gateway must stay reachable", target, host, rec.Body.String())
			}
		}
	}
}

func TestHostGuard_AllowListAndTokenedPublicBind(t *testing.T) {
	t.Setenv("WRONGTRACE_TOKEN", "")
	t.Setenv(allowedHostsEnv, "host.docker.internal, Dev.Box.")
	s, _ := newGuardTestServer(t, Config{Host: "0.0.0.0"})
	for _, host := range []string{"host.docker.internal:3444", "dev.box:3444"} {
		if rec := serve(s, http.MethodGet, "/api/metrics/overview", host, nil); rec.Code != http.StatusOK {
			t.Errorf("allow-listed Host %q = %d, want 200", host, rec.Code)
		}
	}
	if rec := serve(s, http.MethodGet, "/api/metrics/overview", "evil.example:3444", nil); rec.Code != http.StatusForbidden {
		t.Errorf("unlisted Host on untokened public bind = %d, want 403", rec.Code)
	}

	// With a token on a non-loopback bind any Host is fine: the token guards.
	t.Setenv(allowedHostsEnv, "")
	st, _ := newGuardTestServer(t, Config{Host: "0.0.0.0", AuthToken: "sekret"})
	rec := serve(st, http.MethodGet, "/api/metrics/overview", "myhost.lan:3444", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer sekret")
	})
	if rec.Code != http.StatusOK {
		t.Errorf("tokened public bind with named Host = %d, want 200", rec.Code)
	}
}

func TestCheckOrigin_SameHostRequiresVerifiedHost(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/ws", nil)
	req.Host = "attacker.test:3444"
	req.Header.Set("Origin", "http://attacker.test:3444")
	if checkOrigin(req) {
		t.Fatal("checkOrigin accepted Origin==Host for a Host hostGuard never vetted")
	}
	verified := req.WithContext(context.WithValue(req.Context(), hostVerifiedKey, true))
	if !checkOrigin(verified) {
		t.Fatal("checkOrigin rejected a same-host origin on a verified Host")
	}
	loop := httptest.NewRequest(http.MethodGet, "/api/ws", nil)
	loop.Header.Set("Origin", "http://localhost:5173")
	if !checkOrigin(loop) {
		t.Fatal("checkOrigin rejected the loopback vite dev origin")
	}
}

// /api/health skips token auth for liveness probes, but it disclosed the repo
// name, IPC socket path, and WebSocket client count to anyone.
func TestHealth_DiagnosticsOnlyForTrustedCallers(t *testing.T) {
	hasDetail := func(rec *httptest.ResponseRecorder) bool {
		var body map[string]interface{}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode health: %v", err)
		}
		if body["service"] != "wrongtrace" || body["ok"] != true || body["status"] != "ok" {
			t.Fatalf("liveness fields missing: %v", body)
		}
		_, repo := body["repo"]
		_, sock := body["socket_path"]
		_, ws := body["ws_clients"]
		return repo || sock || ws
	}

	t.Setenv("WRONGTRACE_TOKEN", "")
	open, _ := newGuardTestServer(t, Config{SocketPath: "/tmp/x.sock"})
	if !hasDetail(serve(open, http.MethodGet, "/api/health", "127.0.0.1:3444", nil)) {
		t.Error("no-token loopback caller lost health diagnostics (dashboard needs socket_path)")
	}
	remote := serve(open, http.MethodGet, "/api/health", "192.168.1.20:3444", func(r *http.Request) {
		r.RemoteAddr = "192.168.1.77:40000"
	})
	if hasDetail(remote) {
		t.Error("no-token remote caller received health diagnostics")
	}

	tokened, _ := newGuardTestServer(t, Config{AuthToken: "sekret", SocketPath: "/tmp/x.sock"})
	if hasDetail(serve(tokened, http.MethodGet, "/api/health", "127.0.0.1:3444", nil)) {
		t.Error("unauthenticated caller on a tokened daemon received health diagnostics")
	}
	if !hasDetail(serve(tokened, http.MethodGet, "/api/health", "127.0.0.1:3444", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer sekret")
	})) {
		t.Error("authenticated caller lost health diagnostics")
	}
}

// Requests must inherit the daemon lifetime context so SSE and WebSocket
// handlers observe shutdown.
func TestNewHTTPServer_UsesConfiguredBaseContext(t *testing.T) {
	t.Setenv("WRONGTRACE_TOKEN", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, _ := newGuardTestServer(t, Config{BaseContext: ctx})
	hs := s.newHTTPServer("127.0.0.1:0")
	if hs.BaseContext == nil || hs.BaseContext(nil) != ctx {
		t.Fatal("http.Server.BaseContext does not return the configured daemon context")
	}
}

// The HTTP lock endpoint now delegates owner-conflict detection to the engine;
// the 409 shape, same-owner refresh, and explicit force must be preserved.
func TestLockFileHTTP_ConflictRefreshAndForce(t *testing.T) {
	t.Setenv("WRONGTRACE_TOKEN", "")
	s, engine := newGuardTestServer(t, Config{})
	if _, err := engine.TryLockFile("a.go", "first", "Agent1", "run-1", 10*time.Minute, false); err != nil {
		t.Fatal(err)
	}
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/guardrail/lock", strings.NewReader(body))
		req.Host = "127.0.0.1:3444"
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, req)
		return rec
	}
	if rec := post(`{"path":"a.go","owner":"Agent2"}`); rec.Code != http.StatusConflict ||
		!strings.Contains(rec.Body.String(), `"owner":"Agent1"`) || !strings.Contains(rec.Body.String(), `"status":"conflict"`) {
		t.Fatalf("conflict = %d %s, want 409 naming Agent1", rec.Code, rec.Body.String())
	}
	if rec := post(`{"path":"a.go","owner":"Agent1","reason":"refresh"}`); rec.Code != http.StatusOK {
		t.Fatalf("same-owner refresh = %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(`{"path":"a.go","owner":"Agent2","force":true}`); rec.Code != http.StatusOK {
		t.Fatalf("forced lock = %d %s", rec.Code, rec.Body.String())
	}
	if _, info := engine.IsFileLocked("a.go"); info.Owner != "Agent2" {
		t.Fatalf("owner after force = %q, want Agent2", info.Owner)
	}
}
