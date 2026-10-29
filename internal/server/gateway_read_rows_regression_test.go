package server

// A gateway exchange with several read tool calls records one read row per
// call, but its single usage block only once (read rows are summed), and
// id-less calls (Gemini functionCall) must not collide on one ReadID.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func gwReadRun(t *testing.T, path, respBody string) (rows int, prompt int64, cost float64) {
	t.Helper()
	for _, k := range []string{"WRONGTRACE_HOME", "HOME", "USERPROFILE", "APPDATA"} {
		t.Setenv(k, t.TempDir())
	}
	t.Setenv("WRONGTRACE_TOKEN", "")
	t.Setenv("WRONGTRACE_PROXY_LOG", "0")
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(respBody))
	}))
	defer up.Close()
	s, e := newGuardTestServer(t, Config{})
	req := httptest.NewRequest(http.MethodPost, "/proxy/"+strings.TrimPrefix(up.URL, "http://")+path,
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"read both"}]}`))
	req.Host = "127.0.0.1:3444"
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Run-ID", "run-gw-reads")
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("proxy -> %d %s", rec.Code, rec.Body.String())
	}
	s.handlers.Proxy.Close() // drain the finalize pipeline (OnTraffic)
	reads, err := e.GetRecentFileReads(100)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range reads {
		prompt += r.PromptTokens
		cost += r.CostUSD
	}
	return len(reads), prompt, cost
}

func TestGatewayReads_UsageOnceAndDistinctIDs(t *testing.T) {
	bad := false
	// CONTROL: one read in the exchange carries the exchange's usage once.
	one := `{"id":"c1","model":"gpt-4o","choices":[{"message":{"role":"assistant","tool_calls":[` +
		`{"id":"t1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.go\"}"}}]}}],` +
		`"usage":{"prompt_tokens":1000,"completion_tokens":10,"total_tokens":1010}}`
	n, p, c := gwReadRun(t, "/v1/chat/completions", one)
	if n != 1 || p != 1000 {
		t.Fatalf("control invalid")
	}
	two := strings.Replace(one, `}}]}}]`, `}},{"id":"t2","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"b.go\"}"}}]}}]`, 1)
	n, p, c2 := gwReadRun(t, "/v1/chat/completions", two)
	t.Logf("EXPECTED (two parallel reads): rows=2 prompt=1000 cost=%.6f", c)
	t.Logf("ACTUAL:   rows=%d prompt=%d cost=%.6f", n, p, c2)
	if n != 2 || p != 1000 {
		bad = true
	}
	gem := `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"read_file","args":{"path":"a.go"}}},` +
		`{"functionCall":{"name":"read_file","args":{"path":"b.go"}}}]},"finishReason":"STOP"}],` +
		`"usageMetadata":{"promptTokenCount":1000,"candidatesTokenCount":10,"totalTokenCount":1010},"modelVersion":"gemini-2.5-pro"}`
	n, p, _ = gwReadRun(t, "/v1beta/models/gemini-2.5-pro:generateContent", gem)
	t.Logf("EXPECTED (Gemini, two id-less reads): rows=2 prompt=1000")
	t.Logf("ACTUAL:   rows=%d prompt=%d", n, p)
	if n != 2 || p != 1000 {
		bad = true
	}
	if bad {
		t.Error("parallel reads multiplied the exchange's usage or collided on one id")
	}
}
