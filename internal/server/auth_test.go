package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wrongstack/wrongtrace/internal/core"
	"github.com/wrongstack/wrongtrace/internal/db"
)

// newAuthedTestServer mirrors newTestServer but turns on token auth by
// seeding WRONGTRACE_TOKEN before New() resolves it.
func newAuthedTestServer(t *testing.T, token string) *httptest.Server {
	t.Helper()
	t.Setenv("WRONGTRACE_TOKEN", token)
	store, err := db.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	engine := core.NewEngine(core.Config{RepoName: "auth-test", Store: store})
	s := New(Config{Port: 0, Engine: engine})
	ts := httptest.NewServer(s.router)
	t.Cleanup(ts.Close)
	return ts
}

func statusOf(t *testing.T, req *http.Request) int {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestTokenAuth_GateWithoutCredentials(t *testing.T) {
	ts := newAuthedTestServer(t, "sekret")

	cases := []struct {
		name string
		req  *http.Request
		want int
	}{
		{"api GET blocked", mustReq(t, http.MethodGet, ts.URL+"/api/metrics/overview"), http.StatusUnauthorized},
		{"api POST blocked", mustReq(t, http.MethodPost, ts.URL+"/api/telemetry"), http.StatusUnauthorized},
		{"guardrail unlock blocked", mustReq(t, http.MethodPost, ts.URL+"/api/guardrail/unlock"), http.StatusUnauthorized},
		{"gateway proxy blocked", mustReq(t, http.MethodGet, ts.URL+"/proxy/api.openai.com/v1/models"), http.StatusUnauthorized},
		{"otlp ingest blocked", mustReq(t, http.MethodPost, ts.URL+"/v1/traces"), http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := statusOf(t, tc.req); got != tc.want {
				t.Errorf("status = %d, want %d", got, tc.want)
			}
		})
	}

	// A wrong bearer token is as good as none.
	req := mustReq(t, http.MethodGet, ts.URL+"/api/metrics/overview")
	req.Header.Set("Authorization", "Bearer wrong-token")
	if got := statusOf(t, req); got != http.StatusUnauthorized {
		t.Errorf("wrong bearer: status = %d, want 401", got)
	}
}

func TestTokenAuth_LivenessAndShellStayOpen(t *testing.T) {
	ts := newAuthedTestServer(t, "sekret")

	var h map[string]interface{}
	resp := getJSON(t, ts.URL+"/api/health", &h)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/api/health = %d, want 200 (liveness exemption)", resp.StatusCode)
	}

	resp, body := getText(t, ts.URL+"/")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "<") {
		t.Errorf("SPA shell = %d (%d bytes), want 200 HTML so the dashboard can load and mint its cookie",
			resp.StatusCode, len(body))
	}
}

func TestTokenAuth_AcceptsEveryCredentialForm(t *testing.T) {
	const token = "sekret"
	ts := newAuthedTestServer(t, token)

	setBearer := func(r *http.Request) { r.Header.Set("Authorization", "bearer "+token) }
	cases := []struct {
		name string
		set  func(*http.Request)
	}{
		{"authorization bearer", setBearer},
		{"x-wrongtrace-token header", func(r *http.Request) { r.Header.Set("X-WrongTrace-Token", token) }},
		{"query parameter", func(r *http.Request) {
			r.URL.RawQuery = "token=" + token
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := mustReq(t, http.MethodGet, ts.URL+"/api/metrics/recent?limit=1")
			tc.set(req)
			if got := statusOf(t, req); got != http.StatusOK {
				t.Errorf("status = %d, want 200", got)
			}
		})
	}
}

func TestTokenAuth_LoginMintsSessionCookie(t *testing.T) {
	ts := newAuthedTestServer(t, "sekret")

	// Wrong token is refused outright.
	if got := statusOf(t, mustReq(t, http.MethodGet, ts.URL+"/auth?token=nope")); got != http.StatusForbidden {
		t.Fatalf("bad /auth login = %d, want 403", got)
	}

	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse // do not follow the "/" redirect
		},
	}
	resp, err := client.Get(ts.URL + "/auth?token=sekret")
	if err != nil {
		t.Fatalf("GET /auth: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("/auth login = %d, want 303", resp.StatusCode)
	}
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == authCookieName {
			session = c
		}
	}
	if session == nil {
		t.Fatal("/auth login did not set a session cookie")
	}
	if !session.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}

	// The cookie alone authenticates subsequent API calls.
	req := mustReq(t, http.MethodGet, ts.URL+"/api/metrics/recent?limit=1")
	req.AddCookie(session)
	if got := statusOf(t, req); got != http.StatusOK {
		t.Errorf("cookie-authenticated API call = %d, want 200", got)
	}

	// The cookie value is a per-process nonce, not the token itself.
	if strings.Contains(session.Value, "sekret") {
		t.Error("session cookie leaks the configured token")
	}
}

// The historical behavior must not change when no token is configured.
func TestTokenAuth_InactiveWhenUnset(t *testing.T) {
	_, _, ts := newTestServer(t)

	req := mustReq(t, http.MethodGet, ts.URL+"/api/metrics/overview")
	if got := statusOf(t, req); got != http.StatusOK {
		t.Fatalf("unauthenticated API call without token config = %d, want 200", got)
	}
}

func mustReq(t *testing.T, method, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	return req
}
