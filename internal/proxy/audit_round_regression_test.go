package proxy

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// auditServe drives one request through ServeHTTP and drains finalize.
func auditServe(p *GatewayProxy, method, target, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, req)
	p.waitFinalize()
	return rr
}

// TestCacheKey_BindsMethodPathQueryAndUpstream pins finding 1: the response
// cache key hashed only provider/model/scope/body, so identical bodies sent to
// different endpoints the body does not name shared one cache entry.
func TestCacheKey_BindsMethodPathQueryAndUpstream(t *testing.T) {
	t.Run("gemini verb in path", func(t *testing.T) {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"path":"`+r.URL.Path+`"}`)
		}))
		t.Cleanup(up.Close)
		p := NewGatewayProxy(Config{})
		t.Cleanup(p.Close)
		body := `{"contents":[{"parts":[{"text":"hi"}]}]}`
		hdr := map[string]string{"X-Target-Upstream": up.URL, "X-WrongTrace-Cache": "allow"}

		_ = auditServe(p, http.MethodPost, "/v1beta/models/gemini-pro:countTokens", body, hdr)
		gen := auditServe(p, http.MethodPost, "/v1beta/models/gemini-pro:generateContent", body, hdr)
		if gen.Header().Get("X-WrongTrace-Cache") == "HIT" || !strings.Contains(gen.Body.String(), "generateContent") {
			t.Fatalf("generateContent served the countTokens cache entry: cache=%q body=%s", gen.Header().Get("X-WrongTrace-Cache"), gen.Body.String())
		}
		// Control: the cache still works for a genuinely identical request.
		again := auditServe(p, http.MethodPost, "/v1beta/models/gemini-pro:generateContent", body, hdr)
		if again.Header().Get("X-WrongTrace-Cache") != "HIT" {
			t.Fatalf("identical request missed the cache")
		}
	})

	t.Run("alt=sse query", func(t *testing.T) {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("alt") == "sse" {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"sse\"}]}}]}\n\n")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[{"candidates":[{"content":{"parts":[{"text":"array"}]}}]}]`)
		}))
		t.Cleanup(up.Close)
		p := NewGatewayProxy(Config{})
		t.Cleanup(p.Close)
		body := `{"contents":[{"parts":[{"text":"hi"}]}]}`
		hdr := map[string]string{"X-Target-Upstream": up.URL, "X-WrongTrace-Cache": "allow"}
		const path = "/v1beta/models/gemini-pro:streamGenerateContent"

		_ = auditServe(p, http.MethodPost, path+"?alt=sse", body, hdr)
		arr := auditServe(p, http.MethodPost, path, body, hdr)
		if arr.Header().Get("X-WrongTrace-Cache") == "HIT" || !strings.Contains(arr.Body.String(), "array") {
			t.Fatalf("JSON-array stream served the alt=sse cache entry: %s", arr.Body.String())
		}
		hit := auditServe(p, http.MethodPost, path, body, hdr)
		if hit.Header().Get("X-WrongTrace-Cache") != "HIT" {
			t.Fatalf("clean JSON-array stream was not cached")
		}
		if ct := hit.Result().Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Fatalf("replayed JSON-array stream Content-Type = %q, want application/json", ct)
		}
	})

	t.Run("distinct upstream hosts", func(t *testing.T) {
		mk := func(tag string) *httptest.Server {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"from":"`+tag+`"}`)
			}))
			t.Cleanup(s.Close)
			return s
		}
		upA, upB := mk("A"), mk("B")
		p := NewGatewayProxy(Config{})
		t.Cleanup(p.Close)
		body := `{"model":"m","messages":[]}`
		_ = auditServe(p, http.MethodPost, "/v1/chat/completions", body, map[string]string{"X-Target-Upstream": upA.URL, "X-WrongTrace-Cache": "allow"})
		b := auditServe(p, http.MethodPost, "/v1/chat/completions", body, map[string]string{"X-Target-Upstream": upB.URL, "X-WrongTrace-Cache": "allow"})
		if !strings.Contains(b.Body.String(), `"B"`) {
			t.Fatalf("upstream B request served upstream A's cached response: cache=%q body=%s", b.Header().Get("X-WrongTrace-Cache"), b.Body.String())
		}
	})
}

// TestComputeRequestKey_FieldsAreIsolated is the unit-level companion.
func TestComputeRequestKey_FieldsAreIsolated(t *testing.T) {
	body := []byte(`{}`)
	base := ComputeRequestKey("P", "m", "s", "POST", "/a", "http://h/a", body)
	for name, k := range map[string]string{
		"method": ComputeRequestKey("P", "m", "s", "PUT", "/a", "http://h/a", body),
		"path":   ComputeRequestKey("P", "m", "s", "POST", "/b", "http://h/a", body),
		"target": ComputeRequestKey("P", "m", "s", "POST", "/a", "http://h/a?alt=sse", body),
		"shift":  ComputeRequestKey("P", "m", "s", "POST/a", "", "http://h/a", body),
	} {
		if k == base {
			t.Errorf("%s change did not change the key", name)
		}
	}
	if base == ComputeScopedKey("P", "m", "s", body) {
		t.Error("request key collides with the scoped key")
	}
}

// TestStreaming_TruncatedStreamIsNotCached pins finding 2: a stream cut by an
// upstream reset was cached and replayed as a complete 200.
func TestStreaming_TruncatedStreamIsNotCached(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
		w.(http.Flusher).Flush()
		if r.Header.Get("X-Clean") == "1" {
			return
		}
		c, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = c.Close() // reset mid-stream: no terminating chunk
		}
	}))
	t.Cleanup(up.Close)
	p := NewGatewayProxy(Config{})
	t.Cleanup(p.Close)
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)

	do := func(clean bool) string {
		body := `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"x"}]}`
		if clean {
			body = `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"clean"}]}`
		}
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("X-Target-Upstream", up.URL)
		req.Header.Set("X-WrongTrace-Cache", "allow")
		if clean {
			req.Header.Set("X-Clean", "1")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		p.waitFinalize()
		return resp.Header.Get("X-WrongTrace-Cache")
	}

	_ = do(false)
	if recs := p.AllTraffic(1); len(recs) != 1 || !recs[0].Truncated {
		t.Fatalf("reset stream not marked truncated: %+v", recs)
	}
	if got := do(false); got == "HIT" {
		t.Fatalf("truncated stream was served from cache")
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls.Load())
	}

	// Control: a clean stream is still cached and not marked truncated.
	_ = do(true)
	if recs := p.AllTraffic(1); recs[0].Truncated {
		t.Fatalf("clean stream marked truncated")
	}
	if got := do(true); got != "HIT" {
		t.Fatalf("clean stream was not cached (cache=%q)", got)
	}
}

// readFirstChunkWhileUpstreamOpen proves incremental relay: the first body
// bytes must reach the client while the upstream handler is still blocked.
func readFirstChunkWhileUpstreamOpen(t *testing.T, req *http.Request, upstreamDone <-chan struct{}) (*http.Response, string) {
	t.Helper()
	type doRes struct {
		resp *http.Response
		err  error
	}
	doCh := make(chan doRes, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		doCh <- doRes{resp, err}
	}()
	var resp *http.Response
	select {
	case r := <-doCh:
		if r.err != nil {
			t.Fatalf("request: %v", r.err)
		}
		resp = r.resp
	case <-time.After(5 * time.Second):
		t.Fatal("no response headers within 5s while upstream stream was open (buffered)")
	}
	readCh := make(chan string, 1)
	go func() {
		buf := make([]byte, 512)
		n, _ := resp.Body.Read(buf)
		readCh <- string(buf[:n])
	}()
	select {
	case s := <-readCh:
		select {
		case <-upstreamDone:
			t.Fatal("first bytes arrived only after the upstream completed")
		default:
		}
		return resp, s
	case <-time.After(5 * time.Second):
		_ = resp.Body.Close()
		t.Fatal("no body bytes within 5s while upstream stream was open (buffered)")
	}
	return nil, ""
}

// heldStreamUpstream writes first, flushes, waits for release, writes rest.
func heldStreamUpstream(t *testing.T, contentType, first, rest string) (url string, release func(), done <-chan struct{}) {
	t.Helper()
	rel := make(chan struct{})
	var once sync.Once
	doneCh := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(doneCh)
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, first)
		w.(http.Flusher).Flush()
		<-rel
		_, _ = io.WriteString(w, rest)
	}))
	t.Cleanup(up.Close)
	release = func() { once.Do(func() { close(rel) }) }
	return up.URL, release, doneCh
}

// TestStreaming_OllamaNDJSONRelayedIncrementally pins finding 3 for Ollama's
// native API: /api/chat streams NDJSON by default with no "stream" field and
// was buffered until generation finished. Usage analysis must still work.
func TestStreaming_OllamaNDJSONRelayedIncrementally(t *testing.T) {
	upURL, release, done := heldStreamUpstream(t, "application/x-ndjson",
		`{"model":"llama3","message":{"role":"assistant","content":"Hel"},"done":false}`+"\n",
		`{"model":"llama3","message":{"role":"assistant","content":"lo"},"done":true,"done_reason":"stop","prompt_eval_count":26,"eval_count":7}`+"\n")
	p := NewGatewayProxy(Config{})
	t.Cleanup(p.Close)
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	t.Cleanup(release)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/chat", strings.NewReader(`{"model":"llama3","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("X-Target-Upstream", upURL)
	resp, first := readFirstChunkWhileUpstreamOpen(t, req, done)
	if !strings.Contains(first, "Hel") {
		t.Fatalf("first chunk = %q", first)
	}
	release()
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Fatalf("NDJSON stream relabeled: Content-Type = %q", ct)
	}
	p.waitFinalize()
	recs := p.AllTraffic(1)
	if len(recs) != 1 {
		t.Fatalf("records = %d", len(recs))
	}
	rec := recs[0]
	if !rec.IsStream || rec.AssistantReply != "Hello" || rec.PromptTokens != 26 || rec.CompletionTokens != 7 || rec.FinishReason != "stop" {
		t.Fatalf("NDJSON analysis wrong: stream=%v reply=%q in=%d out=%d finish=%q", rec.IsStream, rec.AssistantReply, rec.PromptTokens, rec.CompletionTokens, rec.FinishReason)
	}
}

// TestStreaming_GeminiJSONArrayRelayedIncrementally pins finding 3 for
// Gemini streamGenerateContent without alt=sse.
func TestStreaming_GeminiJSONArrayRelayedIncrementally(t *testing.T) {
	upURL, release, done := heldStreamUpstream(t, "application/json; charset=UTF-8",
		`[{"candidates":[{"content":{"parts":[{"text":"Hel"}],"role":"model"}}]}`,
		"\n"+`,{"candidates":[{"content":{"parts":[{"text":"lo"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":2,"totalTokenCount":13},"modelVersion":"gemini-1.5-flash"}]`)
	p := NewGatewayProxy(Config{})
	t.Cleanup(p.Close)
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	t.Cleanup(release)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1beta/models/gemini-1.5-flash:streamGenerateContent", strings.NewReader(`{"contents":[{"parts":[{"text":"hi"}]}]}`))
	req.Header.Set("X-Target-Upstream", upURL)
	resp, first := readFirstChunkWhileUpstreamOpen(t, req, done)
	if !strings.Contains(first, "Hel") {
		t.Fatalf("first chunk = %q", first)
	}
	release()
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("JSON-array stream relabeled: Content-Type = %q", ct)
	}
	p.waitFinalize()
	rec := p.AllTraffic(1)[0]
	if rec.AssistantReply != "Hello" || rec.PromptTokens != 11 || rec.CompletionTokens != 2 || rec.Model != "gemini-1.5-flash" || rec.FinishReason != "STOP" {
		t.Fatalf("Gemini array analysis wrong: reply=%q in=%d out=%d model=%q finish=%q", rec.AssistantReply, rec.PromptTokens, rec.CompletionTokens, rec.Model, rec.FinishReason)
	}
}

func TestAnalyzeWirePayloads_NDJSONAndGeminiArray(t *testing.T) {
	gen := AnalyzeWirePayloads([]byte(`{"model":"llama3","prompt":"hi"}`),
		[]byte(`{"model":"llama3","response":"foo","done":false}`+"\n"+`{"model":"llama3","response":"bar","done":true,"done_reason":"length","prompt_eval_count":3,"eval_count":9}`+"\n"), true)
	if gen.AssistantReply != "foobar" || gen.PromptTokens != 3 || gen.CompletionTokens != 9 || gen.TotalTokens != 12 || gen.FinishReason != "length" {
		t.Fatalf("ollama generate: %+v", gen)
	}
	gem := AnalyzeWirePayloads(nil, []byte(`[{"candidates":[{"content":{"parts":[{"text":"think","thought":true},{"text":"answer"}]}}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":4}}]`), true)
	if gem.AssistantReply != "answer" || gem.Reasoning != "think" || gem.PromptTokens != 5 || gem.CompletionTokens != 4 {
		t.Fatalf("gemini array: %+v", gem)
	}
	// Anthropic message_start's array-valued message.content must not break
	// the chunk decode that carries its usage.
	anth := AnalyzeWirePayloads(nil, []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude\",\"content\":[],\"usage\":{\"input_tokens\":12,\"output_tokens\":1}}}\n\n"), true)
	if anth.WireID != "msg_1" || anth.PromptTokens != 12 {
		t.Fatalf("anthropic message_start: %+v", anth)
	}
}

// budgetPost sends one enforce-policy request; safe to call from goroutines.
func budgetPost(baseURL, slug, body string) int {
	req, err := http.NewRequest(http.MethodPost, baseURL+"/proxy/quota/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		return -1
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-WrongTrace-Policy", "enforce")
	req.Header.Set("X-Project-Slug", slug)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return -1
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// TestQuotaGate_ConcurrentRequestsReserveBudget pins finding 4: the check-only
// gate admitted every concurrent request against the same headroom before any
// of them was charged. With reservation only one request fits.
func TestQuotaGate_ConcurrentRequestsReserveBudget(t *testing.T) {
	rel := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(rel) }) }
	var hits atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-rel
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-r","model":"gpt-4o","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1200,"completion_tokens":150}}`)
	}))
	t.Cleanup(up.Close)
	p := NewGatewayProxy(Config{CustomUpstreams: map[string]string{"quota": up.URL}})
	t.Cleanup(p.Close)
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	t.Cleanup(release)

	estimate := estimateRequestCostUSD("Quota", "gpt-4o", []byte(quotaGateBody), ptrFloat(4096))
	if estimate <= 0 {
		t.Fatalf("estimate = %v", estimate)
	}
	p.Quotas.SetBudget("race-tenant", estimate*1.5)

	const n = 5
	statuses := make(chan int, n)
	for i := 0; i < n; i++ {
		go func() { statuses <- budgetPost(srv.URL, "race-tenant", quotaGateBody) }()
	}
	deadline := time.After(10 * time.Second)
	for i := 0; i < n-1; i++ {
		select {
		case s := <-statuses:
			if s != http.StatusTooManyRequests {
				t.Fatalf("concurrent request %d got %d, want 429 (budget fits one reservation)", i, s)
			}
		case <-deadline:
			t.Fatalf("only %d of %d over-budget requests were refused while one was in flight; the gate is not reserving", i, n-1)
		}
	}
	release()
	select {
	case s := <-statuses:
		if s != http.StatusOK {
			t.Fatalf("admitted request got %d", s)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("admitted request never completed")
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("upstream hits = %d, want 1", got)
	}

	p.waitFinalize()
	spend, _ := p.Quotas.GetSpend("race-tenant")
	rec := p.AllTraffic(1)[0]
	if rec.CostUSD <= 0 || math.Abs(spend-rec.CostUSD) > 1e-9 {
		t.Fatalf("reservation not reconciled to actual cost: spend=%v actual=%v estimate=%v", spend, rec.CostUSD, estimate)
	}
}

// TestQuotaGate_ReservationRefundedOnFailure pins the refund half of finding 4.
func TestQuotaGate_ReservationRefundedOnFailure(t *testing.T) {
	t.Run("upstream unreachable", func(t *testing.T) {
		dead := httptest.NewServer(http.NotFoundHandler())
		deadURL := dead.URL
		dead.Close()
		p := NewGatewayProxy(Config{CustomUpstreams: map[string]string{"quota": deadURL}})
		t.Cleanup(p.Close)
		srv := httptest.NewServer(p)
		t.Cleanup(srv.Close)
		p.Quotas.SetBudget("fail-tenant", 100)
		if s := budgetPost(srv.URL, "fail-tenant", quotaGateBody); s != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", s)
		}
		p.waitFinalize()
		if spend, _ := p.Quotas.GetSpend("fail-tenant"); spend != 0 {
			t.Fatalf("failed request kept its reservation: spend=%v", spend)
		}
		if spend, _ := p.Quotas.GetSpend(globalBudgetKey); spend != 0 {
			t.Fatalf("global meter kept the reservation: spend=%v", spend)
		}
	})
	t.Run("upstream error status", func(t *testing.T) {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"message":"boom"}}`)
		}))
		t.Cleanup(up.Close)
		p := NewGatewayProxy(Config{CustomUpstreams: map[string]string{"quota": up.URL}})
		t.Cleanup(p.Close)
		srv := httptest.NewServer(p)
		t.Cleanup(srv.Close)
		p.Quotas.SetBudget("err-tenant", 100)
		if s := budgetPost(srv.URL, "err-tenant", quotaGateBody); s != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", s)
		}
		p.waitFinalize()
		if spend, _ := p.Quotas.GetSpend("err-tenant"); spend != 0 {
			t.Fatalf("non-billable upstream error consumed budget: spend=%v", spend)
		}
	})
}

// TestCacheHit_ReplaysRawUpstreamHeaders pins finding 5: hits replayed the
// masked, comma-joined record copy of the headers (including Set-Cookie).
func TestCacheHit_ReplaysRawUpstreamHeaders(t *testing.T) {
	token := fakeTok("abcd", "efgh", "ijklmnop")
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("X-Multi", "alpha")
		w.Header().Add("X-Multi", "beta")
		w.Header().Set("X-Auth-Token", token)
		w.Header().Add("Set-Cookie", "__cf_bm=abcdefghijklmnopqrstuvwxyz; Path=/")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","choices":[{"message":{"content":"ok"}}]}`)
	}))
	t.Cleanup(up.Close)
	p := NewGatewayProxy(Config{})
	t.Cleanup(p.Close)
	hdr := map[string]string{"X-Target-Upstream": up.URL, "X-WrongTrace-Cache": "allow"}
	_ = auditServe(p, http.MethodPost, "/v1/chat/completions", `{"model":"m"}`, hdr)
	hit := auditServe(p, http.MethodPost, "/v1/chat/completions", `{"model":"m"}`, hdr)
	h := hit.Result().Header
	if h.Get("X-WrongTrace-Cache") != "HIT" {
		t.Fatal("expected a cache hit")
	}
	if got := h.Values("X-Multi"); len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("multi-value header replayed as %q", got)
	}
	if got := h.Get("X-Auth-Token"); got != token {
		t.Fatalf("header replayed masked: %q", got)
	}
	if got := h.Values("Set-Cookie"); len(got) != 0 {
		t.Fatalf("Set-Cookie replayed to another caller: %q", got)
	}
	if rec := p.AllTraffic(1)[0]; strings.Contains(rec.ResponseHeaders["X-Auth-Token"], token) {
		t.Fatalf("hit record stored the unmasked header")
	}
}

// TestStreamingRequest_UpstreamJSONErrorKeepsContentType pins finding 6.
func TestStreamingRequest_UpstreamJSONErrorKeepsContentType(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"bad"}}`)
	}))
	t.Cleanup(up.Close)
	p := NewGatewayProxy(Config{})
	t.Cleanup(p.Close)
	rr := auditServe(p, http.MethodPost, "/v1/chat/completions", `{"model":"m","stream":true}`, map[string]string{"X-Target-Upstream": up.URL})
	res := rr.Result()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("JSON error relabeled as %q", ct)
	}
	if cc := res.Header.Get("Cache-Control"); cc == "no-cache" {
		t.Fatalf("SSE Cache-Control applied to an error response")
	}
	if !strings.Contains(rr.Body.String(), `"bad"`) {
		t.Fatalf("error body not relayed: %s", rr.Body.String())
	}
}

// TestFinalize_EnqueueAfterCloseDoesNotPanic pins finding 7: a handler that
// outlived a timed-out HTTP shutdown panicked sending on the closed channel.
func TestFinalize_EnqueueAfterCloseDoesNotPanic(t *testing.T) {
	p := NewGatewayProxy(Config{})
	p.Close()
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("enqueue after Close panicked: %v", r)
			}
		}()
		p.enqueueFinalize(finalizeJob{rec: ProxyTrafficRecord{ID: "late"}})
	}()
	if p.finalizeDropped.Load() != 1 {
		t.Fatalf("late job not counted as dropped")
	}
	p.Close() // idempotent

	// Concurrent enqueuers racing Close (run under -race).
	q := NewGatewayProxy(Config{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				q.enqueueFinalize(finalizeJob{rec: ProxyTrafficRecord{ID: "racer"}})
			}
		}()
	}
	q.Close()
	wg.Wait()
}

// TestUpstreamError_RecordCarriesAttributionAndValidJSON pins finding 8.
func TestUpstreamError_RecordCarriesAttributionAndValidJSON(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	p := NewGatewayProxy(Config{})
	t.Cleanup(p.Close)
	rr := auditServe(p, http.MethodPost, "/v1/chat/completions", `{"model":"m","stream":true}`, map[string]string{
		"X-Target-Upstream": deadURL,
		"X-Agent-Name":      "agent-x",
		"X-Project-ID":      "proj-id",
		"X-Project-Slug":    "proj-slug",
		"X-Task-ID":         "task-9",
		"X-Session-ID":      "sess-7",
	})
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d", rr.Code)
	}
	rec := p.AllTraffic(1)[0]
	if rec.AgentName != "agent-x" || rec.ProjectID != "proj-id" || rec.ProjectSlug != "proj-slug" ||
		rec.TaskID != "task-9" || rec.RunID != "sess-7" || rec.SessionKey == "" || !rec.IsStream {
		t.Fatalf("502 record missing attribution: %+v", rec)
	}
	// The Go client error embeds the URL in quotes (Post "http://..."),
	// which the old string splice turned into invalid JSON.
	if !json.Valid([]byte(rec.ResponseBody)) {
		t.Fatalf("502 record body is not valid JSON: %s", rec.ResponseBody)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(rec.ResponseBody), &body); err != nil || !strings.Contains(body["error"], "127.0.0.1") {
		t.Fatalf("502 record body lost the error: %s (%v)", rec.ResponseBody, err)
	}
}

// TestCacheHit_RecordContentsAfterDeferredAnalysis pins finding 9's
// constraint: moving hit analysis off the request goroutine must leave the
// record identical.
func TestCacheHit_RecordContentsAfterDeferredAnalysis(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"cmpl-1","model":"gpt-4o","choices":[{"message":{"role":"assistant","content":"Cached output"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":20}}`)
	}))
	t.Cleanup(up.Close)
	p := NewGatewayProxy(Config{})
	t.Cleanup(p.Close)
	hdr := map[string]string{"X-Target-Upstream": up.URL, "X-WrongTrace-Cache": "allow"}
	body := `{"model":"gpt-4o","messages":[{"role":"system","content":"sys"},{"role":"user","content":"hello"}]}`
	_ = auditServe(p, http.MethodPost, "/v1/chat/completions", body, hdr)
	hit := auditServe(p, http.MethodPost, "/v1/chat/completions", body, hdr)
	if hit.Header().Get("X-WrongTrace-Cache") != "HIT" || !strings.Contains(hit.Body.String(), "Cached output") {
		t.Fatalf("expected replayed hit, got %q %s", hit.Header().Get("X-WrongTrace-Cache"), hit.Body.String())
	}
	rec := p.AllTraffic(1)[0]
	if !strings.HasPrefix(rec.ID, "px-") || rec.CostUSD != 0 || rec.CacheHitRate != 100 ||
		rec.PromptTokens != 100 || rec.CompletionTokens != 20 || rec.TotalTokens != 120 || rec.CachedTokens != 120 ||
		rec.AssistantReply != "Cached output" || rec.FinishReason != "stop" || rec.MessageCount != 2 || rec.SystemPrompt != "sys" ||
		!strings.Contains(rec.ResponseBody, "Cached output") || !strings.Contains(rec.RequestBody, "hello") || rec.Model != "gpt-4o" {
		t.Fatalf("cache-hit record changed: %+v", rec)
	}
}
