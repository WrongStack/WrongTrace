package server

// With WRONGTRACE_TOKEN set, /proxy/* requires the daemon token. Whatever
// carrier authenticated the request (?token=, Authorization: Bearer, the
// session cookie) must not reach the third-party upstream, while the
// provider's own credentials and unrelated query parameters/cookies must.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestProxyGateway_DaemonTokenNeverForwarded(t *testing.T) {
	for _, k := range []string{"WRONGTRACE_HOME", "HOME", "USERPROFILE", "APPDATA"} {
		t.Setenv(k, t.TempDir())
	}
	t.Setenv("WRONGTRACE_TOKEN", "")
	t.Setenv("WRONGTRACE_PROXY_LOG", "0")
	const daemonToken = "wt-daemon-admin-token-0123456789"

	var mu sync.Mutex
	var seenQuery, seenAuth, seenCookie string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenQuery, seenAuth, seenCookie = r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("Cookie")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-3-7-sonnet","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":5,"output_tokens":1}}`))
	}))
	defer up.Close()
	upHost := strings.TrimPrefix(up.URL, "http://")

	s, _ := newGuardTestServer(t, Config{AuthToken: daemonToken})
	defer s.handlers.Proxy.Close()

	send := func(name, query string, mutate func(*http.Request)) (int, string, string) {
		req := httptest.NewRequest(http.MethodPost, "/proxy/"+upHost+"/v1/messages"+query,
			strings.NewReader(`{"model":"claude-3-7-sonnet","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
		req.Host = "127.0.0.1:3444"
		req.RemoteAddr = "127.0.0.1:50000"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", "sk-ant-provider-key")
		mutate(req)
		mu.Lock()
		seenQuery, seenAuth, seenCookie = "", "", ""
		mu.Unlock()
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, req)
		mu.Lock()
		defer mu.Unlock()
		t.Logf("%-28s -> HTTP %d | upstream saw query=%q Authorization=%q", name, rec.Code, seenQuery, seenAuth)
		return rec.Code, seenQuery, seenAuth
	}

	// CONTROL: unauthenticated is rejected; X-WrongTrace-Token is accepted and stripped.
	if code, _, _ := send("no token", "", func(*http.Request) {}); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated proxy call = %d, want 401", code)
	}
	code, q, a := send("X-WrongTrace-Token header", "", func(r *http.Request) { r.Header.Set("X-WrongTrace-Token", daemonToken) })
	if code != http.StatusOK || strings.Contains(q+a, daemonToken) {
		t.Fatalf("header control code=%d leaked=%v", code, strings.Contains(q+a, daemonToken))
	}

	_, q1, a1 := send("?token= query", "?alt=sse&token="+daemonToken+"&key=v", func(*http.Request) {})
	if strings.Contains(q1+a1, daemonToken) || q1 != "alt=sse&key=v" {
		t.Errorf("?token=: upstream query %q auth %q; want the token removed and alt=sse&key=v kept", q1, a1)
	}
	_, q2, a2 := send("Authorization: Bearer", "", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+daemonToken) })
	if strings.Contains(q2+a2, daemonToken) {
		t.Errorf("Bearer daemon token forwarded upstream: %q", a2)
	}
	// A provider key in Authorization travels on (daemon token in its own header).
	_, _, a3 := send("provider Bearer key", "", func(r *http.Request) {
		r.Header.Set("X-WrongTrace-Token", daemonToken)
		r.Header.Set("Authorization", "Bearer sk-provider-key")
	})
	if a3 != "Bearer sk-provider-key" {
		t.Errorf("provider Authorization = %q; want it forwarded unchanged", a3)
	}
	// The session cookie is dropped; other cookies survive.
	s.sessionNonce = "nonce-for-test"
	send("session cookie", "", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: authCookieName, Value: "nonce-for-test"})
		r.AddCookie(&http.Cookie{Name: "other", Value: "1"})
	})
	mu.Lock()
	cookie := seenCookie
	mu.Unlock()
	if strings.Contains(cookie, authCookieName) || !strings.Contains(cookie, "other=1") {
		t.Errorf("upstream Cookie = %q; want only other=1", cookie)
	}
}
