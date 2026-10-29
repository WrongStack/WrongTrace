package proxy

// Regression (audit F143): in policy-enforce mode the gateway injects the OpenAI-only
// "stream_options":{"include_usage":true} into ANY body that merely contains
// the bytes `"stream"` while isStream is true -- including Anthropic
// /v1/messages requests and requests whose body says "stream": false (isStream
// came only from an Accept: text/event-stream header). The code comment scopes
// this to "OpenAI-compatible streaming"; expectsDoneMarker in the same file
// already distinguishes /messages as non-OpenAI.
//
// External-format dependency (not provable offline): Anthropic's Messages API
// rejects unknown top-level fields, and OpenAI rejects stream_options when
// stream is not true; either makes the forwarded request fail with a 400.

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func f143Forward(t *testing.T, path, body, accept string) string {
	got := make(chan string, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer up.Close()
	p := NewGatewayProxy(Config{CustomUpstreams: map[string]string{"up": up.URL}})
	defer p.Close()
	srv := httptest.NewServer(p)
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/proxy/up"+path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-WrongTrace-Policy", "enforce")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	p.waitFinalize()
	return <-got
}

func TestStreamOptionsOnlyForChatCompletionsStreams(t *testing.T) {
	ctrl := f143Forward(t, "/v1/chat/completions", `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`, "")
	t.Logf("CONTROL (OpenAI chat, stream:true): injected=%v (intended)\n", strings.Contains(ctrl, "stream_options"))

	failed := false
	anth := f143Forward(t, "/v1/messages", `{"model":"claude-sonnet-4-5","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, "")
	t.Log("ANTHROPIC /v1/messages EXPECTED: forwarded body has no stream_options")
	t.Logf("ANTHROPIC /v1/messages ACTUAL:   %s\n", anth)
	if strings.Contains(anth, "stream_options") {
		failed = true
	}
	nonStream := f143Forward(t, "/v1/chat/completions", `{"model":"gpt-4o","stream":false,"messages":[{"role":"user","content":"hi"}]}`, "text/event-stream")
	t.Log("stream:false + Accept SSE EXPECTED: forwarded body has no stream_options")
	t.Logf("stream:false + Accept SSE ACTUAL:   %s\n", nonStream)
	if strings.Contains(nonStream, "stream_options") {
		failed = true
	}
	if failed {
		t.Log("PROBLEM CONFIRMED")
		t.FailNow()
	}
	t.Log("PROBLEM NOT REPRODUCED")
}
