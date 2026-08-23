package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wrongstack/wrongtrace/internal/ipc"
)

type fakeReporter struct {
	reports []ipc.TelemetryReport
}

func (f *fakeReporter) ReportRun(p ipc.TelemetryReport) error {
	f.reports = append(f.reports, p)
	return nil
}

// fakeTok builds a synthetic secret from parts so no plaintext credential
// pattern appears in this file (secret-scanner friendly test fixtures).
func fakeTok(parts ...string) string { return strings.Join(parts, "") }

// TestMaskHeaders_SecretsRedacted covers the credential header surface: the
// classic auth headers plus provider-specific ones (Gemini x-goog-api-key)
// must never reach traffic records in plaintext.
func TestMaskHeaders_SecretsRedacted(t *testing.T) {
	bearerSecret := fakeTok("sk-ant-", "verysecret", "key1234567890")
	apiKeySecret := fakeTok("sk-", "1234567890", "abcdef")
	googleSecret := fakeTok("AIzaSy", "VerySecret", "GoogleKey987654321")

	h := http.Header{}
	h.Set("Authorization", fakeTok("Bearer", " ", bearerSecret))
	h.Set("X-Api-Key", apiKeySecret)
	h.Set("X-Goog-Api-Key", googleSecret)
	h.Set("Content-Type", "application/json")

	masked := maskHeaders(h)

	if ct := masked["Content-Type"]; ct != "application/json" {
		t.Errorf("non-secret header must pass through, got %q", ct)
	}
	if v := masked["Authorization"]; !strings.HasPrefix(v, "Bearer") || strings.Contains(v, "verysecret") {
		t.Errorf("Authorization not redacted properly: %q", v)
	}
	for k, secret := range map[string]string{
		"X-Api-Key":      apiKeySecret,
		"X-Goog-Api-Key": googleSecret,
	} {
		v, ok := masked[k]
		if !ok || v == "" || v == h.Get(k) || strings.Contains(v, secret) {
			t.Errorf("%s not redacted: %q", k, v)
		}
	}
}

// TestSanitizeURLForRecord covers query-string credential scrubbing for logs
// and traffic records (Gemini-style ?key=..., generic token params).
func TestSanitizeURLForRecord(t *testing.T) {
	gemSecret := fakeTok("AIzaSy", "SECRET", "123456")
	tokSecret := fakeTok("tok-", "secret-", "value")
	apiSecret := fakeTok("abc-", "secret")

	cases := []struct {
		name string
		in   string
		want string // substring that MUST appear
		bad  string // substring that MUST NOT appear
	}{
		{"gemini key", "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-pro:generateContent?key=" + gemSecret, "[redacted]", gemSecret},
		{"access token", "https://api.example.com/v1/x?access_token=" + tokSecret, "[redacted]", tokSecret},
		{"api key snake", "https://api.example.com/v1/x?api_key=" + apiSecret, "[redacted]", apiSecret},
		{"clean url untouched", "https://api.openai.com/v1/chat/completions?foo=bar", "foo=bar", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeURLForRecord(tc.in)
			// url.Values.Encode percent-encodes "[redacted]" as %5Bredacted%5D,
			// so assert on the bare marker word.
			if !strings.Contains(got, "redacted") && tc.bad != "" {
				t.Errorf("got %q, want a redacted credential marker", got)
			}
			if !strings.Contains(got, tc.want) && tc.bad == "" {
				t.Errorf("got %q, want substring %q", got, tc.want)
			}
			if tc.bad != "" && strings.Contains(got, tc.bad) {
				t.Errorf("got %q still contains secret material", got)
			}
		})
	}
}

// TestGatewayProxy_UpstreamErrorDoesNotLeakQueryKey drives a request whose
// target URL carries a ?key=<secret> against an unreachable upstream and
// asserts neither the client response nor the traffic record contains it.
func TestGatewayProxy_UpstreamErrorDoesNotLeakQueryKey(t *testing.T) {
	leakySecret := fakeTok("SECRET-", "leaky-", "value-", "123")

	p := NewGatewayProxy(Config{})
	req := httptest.NewRequest(http.MethodPost, "/proxy/dead/v1/chat/completions?key="+leakySecret, strings.NewReader(`{"model":"gpt-4o"}`))
	req.Header.Set("X-Target-Upstream", "http://127.0.0.1:1/v1") // closed port: guaranteed failure
	rec := httptest.NewRecorder()

	p.ServeHTTP(rec, req)

	if body := rec.Body.String(); strings.Contains(body, leakySecret) {
		t.Errorf("client error response leaks query key: %s", body)
	}

	traffic := p.AllTraffic(0)
	if len(traffic) != 1 {
		t.Fatalf("expected 1 error traffic record, got %d", len(traffic))
	}
	if strings.Contains(traffic[0].TargetURL, leakySecret) {
		t.Errorf("traffic record TargetURL leaks query key: %s", traffic[0].TargetURL)
	}
	if strings.Contains(traffic[0].ResponseBody, leakySecret) {
		t.Errorf("traffic record ResponseBody leaks query key: %s", traffic[0].ResponseBody)
	}
}

// TestSanitizeBodyForRecord_JSONLongStrings verifies long prompt content is
// head/tail-truncated while the stored body remains valid JSON for the
// dashboard inspector's JSON.parse.
func TestSanitizeBodyForRecord_JSONLongStrings(t *testing.T) {
	longPrompt := "Explain " + strings.Repeat("x", 2000)
	m := map[string]interface{}{
		"model":    "gpt-4o",
		"messages": []map[string]string{{"role": "user", "content": longPrompt}},
	}
	raw, _ := json.Marshal(m)

	out := sanitizeBodyForRecord(string(raw))

	var v map[string]interface{}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("sanitized body must stay valid JSON: %v\n%s", err, out)
	}
	msgs, _ := v["messages"].([]interface{})
	if len(msgs) != 1 {
		t.Fatalf("messages lost: %s", out)
	}
	content, _ := msgs[0].(map[string]interface{})["content"].(string)
	if len(content) > maxRecordStringLen+64 {
		t.Errorf("long content not truncated: %d chars", len(content))
	}
	if !strings.HasPrefix(content, "Explain ") {
		t.Errorf("head of prompt lost")
	}
	if !strings.Contains(content, "chars]") {
		t.Errorf("truncation marker missing from content")
	}
	if strings.Count(out, strings.Repeat("x", 600)) > 0 {
		t.Errorf("bulk prompt body still present after masking")
	}
}

// TestSanitizeBodyForRecord_CredentialKeys verifies credential-keyed values
// are fully redacted at any nesting depth. Keys/secrets are assembled at
// runtime so no credential pattern appears in this file's source.
func TestSanitizeBodyForRecord_CredentialKeys(t *testing.T) {
	apiSecret := fakeTok("sk-live-", "cred", "-999")
	pwSecret := fakeTok("pw-", "hunter", "-2")
	credKey := fakeTok("api_", "key")
	pwKey := fakeTok("pass", "word")

	m := map[string]interface{}{
		"model":       "m",
		credKey:       apiSecret,
		"nested":      map[string]string{pwKey: pwSecret},
		"temperature": 0.7,
	}
	raw, _ := json.Marshal(m)

	out := sanitizeBodyForRecord(string(raw))

	var v map[string]interface{}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("sanitized body must stay valid JSON: %v", err)
	}
	if v[credKey] != "[redacted]" {
		t.Errorf("credential key not fully redacted: %v", v[credKey])
	}
	nested, _ := v["nested"].(map[string]interface{})
	if nested == nil || nested[pwKey] != "[redacted]" {
		t.Errorf("nested password not redacted: %v", nested)
	}
	if strings.Contains(out, apiSecret) || strings.Contains(out, pwSecret) {
		t.Errorf("credential values leaked into stored body")
	}
	if v["temperature"] != 0.7 {
		t.Errorf("non-string values must pass through: %v", v["temperature"])
	}
}

// TestSanitizeBodyForRecord_BodySizeCap drives a body whose sanitized form
// still exceeds the total cap and asserts the head/tail truncation marker.
func TestSanitizeBodyForRecord_BodySizeCap(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"messages":[`)
	for i := 0; i < 200; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"content":"head ` + strings.Repeat("y", 700) + ` tail"}`)
	}
	sb.WriteString(`]}`)

	out := sanitizeBodyForRecord(sb.String())
	if len(out) > maxRecordBodyLen+512 {
		t.Errorf("stored body exceeds cap: %d bytes", len(out))
	}
	if !strings.Contains(out, "[body truncated") {
		t.Errorf("body-level truncation marker missing")
	}
}

// TestSanitizeBodyForRecord_SSEStream verifies SSE chunks are masked
// individually, keeping the data:/[DONE] stream structure intact.
func TestSanitizeBodyForRecord_SSEStream(t *testing.T) {
	longContent := strings.Repeat("z", 900)
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"" + longContent + "\"}}]}\n\n" +
		"data: {\"usage\":{\"prompt_tokens\":10}}\n\n" +
		"data: [DONE]\n\n"

	out := sanitizeBodyForRecord(sse)

	if !strings.Contains(out, "data: [DONE]") {
		t.Errorf("terminal [DONE] marker must survive: %q", out)
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") || strings.Contains(line, "[DONE]") {
			continue
		}
		var v interface{}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &v); err != nil {
			t.Errorf("SSE chunk no longer parseable JSON: %q (%v)", line, err)
		}
	}
	if strings.Count(out, strings.Repeat("z", 600)) > 0 {
		t.Errorf("bulk streamed content still present after masking")
	}
}

// TestGatewayProxy_LargePromptStoredMaskedButTelemetryExact is the end-to-end
// invariant: a huge prompt is stored masked, while usage telemetry stays
// exact because analysis runs on the original wire bytes before the
// recordTraffic sanitizing choke point.
func TestGatewayProxy_LargePromptStoredMaskedButTelemetryExact(t *testing.T) {
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-4o","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1000,"completion_tokens":50}}`))
	}))
	defer mockUpstream.Close()

	rep := &fakeReporter{}
	p := NewGatewayProxy(Config{Reporter: rep})

	hugePrompt := "TASK " + strings.Repeat("p", 120*1024)
	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"` + hugePrompt + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/proxy/mock/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("X-Target-Upstream", mockUpstream.URL+"/v1")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	// Stored request body must be capped and masked.
	traffic := p.AllTraffic(0)
	if len(traffic) != 1 {
		t.Fatalf("expected 1 traffic record, got %d", len(traffic))
	}
	stored := traffic[0].RequestBody
	if len(stored) > maxRecordBodyLen+512 {
		t.Errorf("stored RequestBody exceeds cap: %d bytes", len(stored))
	}
	if strings.Count(stored, strings.Repeat("p", 600)) > 0 {
		t.Errorf("bulk prompt content leaked into stored record")
	}

	// Telemetry must stay exact (from upstream usage, not the masked copy).
	if len(rep.reports) != 1 {
		t.Fatalf("expected 1 telemetry report, got %d", len(rep.reports))
	}
	if rep.reports[0].PromptTokens != 1000 || rep.reports[0].CompletionTokens != 50 {
		t.Errorf("telemetry must use exact upstream usage, got %+v", rep.reports[0])
	}
}

// TestSanitizeBodyForRecord_UsageFieldsPreserved is the E2E-discovered
// regression: usage/token-count fields (prompt_tokens, max_tokens…) must NOT
// be redacted as credentials, while real token keys (access_token) still are.
func TestSanitizeBodyForRecord_UsageFieldsPreserved(t *testing.T) {
	accessSecret := fakeTok("ya29.", "real", "-cred")
	tokKey := fakeTok("access_", "token")

	m := map[string]interface{}{
		"usage":      map[string]int{"prompt_tokens": 1000, "completion_tokens": 50},
		"max_tokens": 4096,
		tokKey:       accessSecret,
	}
	raw, _ := json.Marshal(m)
	out := sanitizeBodyForRecord(string(raw))

	var v map[string]interface{}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("valid JSON expected: %v", err)
	}
	usage, _ := v["usage"].(map[string]interface{})
	if usage == nil || usage["prompt_tokens"] != float64(1000) || usage["completion_tokens"] != float64(50) {
		t.Errorf("usage counts must survive masking: %v", usage)
	}
	if v["max_tokens"] != float64(4096) {
		t.Errorf("max_tokens must survive masking: %v", v["max_tokens"])
	}
	if v[tokKey] != "[redacted]" || strings.Contains(out, accessSecret) {
		t.Errorf("real access token must still be redacted: %v", v[tokKey])
	}
}

// TestStripProxyMountLabel covers the addressing-prefix strip for
// X-Target-Upstream requests: the /proxy/<label> tag must go, while /v1
// mounts and embedded-host forms must pass through untouched.
func TestStripProxyMountLabel(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/proxy/mock/v1/chat/completions", "/v1/chat/completions"},
		{"/proxy/mock/", "/"},
		{"/proxy/mock", "/"},
		{"/proxy/enterprise-llm/api/coding/paas/v4/chat/completions", "/api/coding/paas/v4/chat/completions"},
		// No /proxy prefix (mounted at /v1 directly) — unchanged.
		{"/v1/chat/completions", "/v1/chat/completions"},
		// Embedded host/scheme forms — first segment IS addressing, unchanged.
		{"/proxy/127.0.0.1:4819/v1/chat/completions", "/proxy/127.0.0.1:4819/v1/chat/completions"},
		{"/proxy/https://api.z.ai/api/v4/x", "/proxy/https://api.z.ai/api/v4/x"},
		{"/proxy/api.z.ai/api/v4/x", "/proxy/api.z.ai/api/v4/x"},
	}
	for _, tc := range cases {
		if got := stripProxyMountLabel(tc.in); got != tc.want {
			t.Errorf("stripProxyMountLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestGatewayProxy_TargetUpstreamHeaderStripsMountLabel drives a request
// with BOTH the X-Target-Upstream header and a /proxy/<label> path, then
// asserts the mock upstream received the bare API path — not the label.
// This is the E2E-observed 404 regression (path duplication).
func TestGatewayProxy_TargetUpstreamHeaderStripsMountLabel(t *testing.T) {
	var gotPath string
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer mockUpstream.Close()

	p := NewGatewayProxy(Config{})
	req := httptest.NewRequest(http.MethodPost, "/proxy/mock/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o"}`))
	req.Header.Set("X-Target-Upstream", mockUpstream.URL+"/v1")
	rec := httptest.NewRecorder()

	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body: %s)", rec.Code, rec.Body.String())
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("upstream received path %q, want /v1/chat/completions (mount label must not leak)", gotPath)
	}

	// The traffic record should also show the clean target URL.
	traffic := p.AllTraffic(0)
	if len(traffic) != 1 || !strings.HasSuffix(traffic[0].TargetURL, "/v1/chat/completions") {
		t.Errorf("traffic TargetURL = %+v, want suffix /v1/chat/completions", traffic)
	}
}

// TestGatewayProxy_V1MountStillWorks guards the plain /v1 mount form
// (no /proxy prefix): the path must forward unchanged to the header-supplied
// upstream base.
func TestGatewayProxy_V1MountStillWorks(t *testing.T) {
	var gotPath string
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer mockUpstream.Close()

	p := NewGatewayProxy(Config{})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o"}`))
	req.Header.Set("X-Target-Upstream", mockUpstream.URL)
	rec := httptest.NewRecorder()

	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("upstream received path %q, want /v1/chat/completions", gotPath)
	}
}

// TestScrubErrorString covers form-independent credential scrubbing in error
// strings: http.Client errors embed a re-normalized URL (percent-encoded,
// different casing) whose exact string differs from the request URL.
func TestScrubErrorString(t *testing.T) {
	gemSecret := fakeTok("AIzaSy", "SECRET", "456789")
	tokSecret := fakeTok("tok-", "abc-", "xyz")

	cases := []struct {
		name string
		in   string
		bad  string
	}{
		{"plain query key", `Post "https://api.example.com/v1/x?key=` + gemSecret + `": dial tcp`, gemSecret},
		{"percent-encoded form", `Post "https://api.x.com/v1/x?access_token=` + tokSecret + `": timeout`, tokSecret},
		{"mixed case param", `Get "https://y.com/a?API_KEY=` + gemSecret, gemSecret},
		{"no secret untouched", `dial tcp 127.0.0.1:1: connection refused`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scrubErrorString(tc.in)
			if !strings.Contains(got, "[redacted]") && tc.bad != "" {
				t.Errorf("got %q, want redaction marker", got)
			}
			if tc.bad != "" && strings.Contains(got, tc.bad) {
				t.Errorf("got %q still leaks secret material", got)
			}
		})
	}
}

// TestMaskHeaders_MultiValueHeaders verifies each value is redacted
// individually — a joined-then-masked blob would only mask the combined
// string once and leak the remaining Set-Cookie values.
func TestMaskHeaders_MultiValueHeaders(t *testing.T) {
	cookieA := fakeTok("session=", "aaa", "secret1")
	cookieB := fakeTok("session=", "bbb", "secret2")

	h := http.Header{}
	h.Add("Set-Cookie", cookieA)
	h.Add("Set-Cookie", cookieB)

	masked := maskHeaders(h)
	v := masked["Set-Cookie"]
	if strings.Contains(v, "secret1") || strings.Contains(v, "secret2") {
		t.Errorf("multi-value Set-Cookie leaks: %q", v)
	}
}

// TestMaskHeaders_LowercaseBearer ensures "bearer <token>" (lowercase scheme)
// is redacted just like "Bearer <token>".
func TestMaskHeaders_LowercaseBearer(t *testing.T) {
	secret := fakeTok("sk-lower-", "case", "-secret-token-99")
	h := http.Header{}
	h.Set("Authorization", fakeTok("bearer", " ", secret))

	masked := maskHeaders(h)
	if v := masked["Authorization"]; strings.Contains(v, secret) {
		t.Errorf("lowercase bearer leaks secret: %q", v)
	}
}

func TestGatewayProxy_JSONResponse(t *testing.T) {
	// Mock Upstream OpenAI server
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		resp := map[string]interface{}{
			"id": "chatcmpl-test",
			"choices": []map[string]interface{}{
				{"message": map[string]string{"role": "assistant", "content": "Hello from mock OpenAI"}},
			},
			"usage": map[string]int{
				"prompt_tokens":     1200,
				"completion_tokens": 150,
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockUpstream.Close()

	reporter := &fakeReporter{}
	proxy := NewGatewayProxy(Config{
		Reporter: reporter,
		CustomUpstreams: map[string]string{
			"custom": mockUpstream.URL,
		},
	})

	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	reqBody := `{"model":"deepseek-r1","messages":[{"role":"user","content":"Write a function"}]}`
	req, _ := http.NewRequest("POST", proxyServer.URL+"/proxy/custom/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST to proxy: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from proxy, got %d", resp.StatusCode)
	}

	if len(reporter.reports) != 1 {
		t.Fatalf("expected 1 reported run from proxy, got %d", len(reporter.reports))
	}

	rep := reporter.reports[0]
	if rep.ModelName != "deepseek-r1" || rep.Provider != "Custom" {
		t.Errorf("telemetry report mismatch: %+v", rep)
	}
}

func TestDetectProvider(t *testing.T) {
	p := NewGatewayProxy(Config{
		CustomUpstreams: map[string]string{
			"groq":       "https://api.groq.com/openai/v1",
			"openrouter": "https://openrouter.ai/api/v1",
			"anthropic":  "https://api.anthropic.com/v1",
		},
	})

	// 1. Subpath
	req1, _ := http.NewRequest("GET", "/proxy/groq/v1/chat/completions", nil)
	prov, _, _ := p.DetectProvider(req1)
	if !strings.Contains(prov, "Groq") {
		t.Errorf("expected Groq, got %s", prov)
	}

	// 2. Custom header
	req2, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
	req2.Header.Set("X-Target-Upstream", "https://openrouter.ai/api/v1")
	req2.Header.Set("X-Provider-Name", "OpenRouter")
	prov, _, _ = p.DetectProvider(req2)
	if !strings.Contains(prov, "OpenRouter") {
		t.Errorf("expected OpenRouter, got %s", prov)
	}

	// 3. Registered provider path
	req3, _ := http.NewRequest("POST", "/v1/anthropic/messages", nil)
	prov, _, _ = p.DetectProvider(req3)
	if !strings.Contains(prov, "Anthropic") {
		t.Errorf("expected Anthropic, got %s", prov)
	}

	// 4. Dynamic Route (e.g. ZAI)
	p.Routes.UpsertRoute(ProxyRoute{
		Name:           "ZAI",
		PathPrefix:     "/proxy/zai",
		TargetUpstream: "https://api.z.ai/api/coding/paas/v4",
		ProtocolType:   "openai-compatible",
		Enabled:        true,
	})

	req4, _ := http.NewRequest("POST", "/proxy/zai/chat/completions", nil)
	prov4, upstream4, rem4 := p.DetectProvider(req4)
	if prov4 != "ZAI" {
		t.Errorf("expected ZAI provider, got %s", prov4)
	}
	if upstream4 != "https://api.z.ai/api/coding/paas/v4" {
		t.Errorf("expected ZAI upstream, got %s", upstream4)
	}
	if rem4 != "/chat/completions" {
		t.Errorf("expected /chat/completions remaining, got %s", rem4)
	}

	// 5. Direct GET diagnostic check on /proxy/zai
	req5, _ := http.NewRequest("GET", "/proxy/zai", nil)
	w5 := httptest.NewRecorder()
	p.ServeHTTP(w5, req5)
	if w5.Code != http.StatusOK {
		t.Errorf("expected 200 OK diagnostic on /proxy/zai, got %d", w5.Code)
	}
	if !strings.Contains(w5.Body.String(), "active") {
		t.Errorf("expected active JSON diagnostic, got %s", w5.Body.String())
	}

	// 6. Zero-Config Direct URL Passthrough: /proxy/api.z.ai/api/coding/paas/v4
	req6, _ := http.NewRequest("POST", "/proxy/api.z.ai/api/coding/paas/v4/chat/completions", nil)
	prov6, upstream6, rem6 := p.DetectProvider(req6)
	if prov6 != "Z.AI" {
		t.Errorf("expected Z.AI provider, got %s", prov6)
	}
	if upstream6 != "https://api.z.ai" {
		t.Errorf("expected https://api.z.ai upstream, got %s", upstream6)
	}
	if rem6 != "/api/coding/paas/v4/chat/completions" {
		t.Errorf("expected /api/coding/paas/v4/chat/completions remaining, got %s", rem6)
	}

	// 7. Zero-Config with full scheme embedded: /proxy/https://api.groq.com/openai/v1/chat/completions
	req7, _ := http.NewRequest("POST", "/proxy/https://api.groq.com/openai/v1/chat/completions", nil)
	prov7, upstream7, rem7 := p.DetectProvider(req7)
	if prov7 != "Groq" {
		t.Errorf("expected Groq provider, got %s", prov7)
	}
	if upstream7 != "https://api.groq.com" {
		t.Errorf("expected https://api.groq.com upstream, got %s", upstream7)
	}
	if rem7 != "/openai/v1/chat/completions" {
		t.Errorf("expected /openai/v1/chat/completions remaining, got %s", rem7)
	}
}

func TestGatewayProxy_StreamingTerminalMarker(t *testing.T) {
	// Mock Upstream server that sends chunks but forgets the explicit [DONE] line
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"package main\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"\\nfunc main() {}\"}}]}\n\n"))
		// Upstream abruptly closes without data: [DONE]
	}))
	defer mockUpstream.Close()

	reporter := &fakeReporter{}
	proxy := NewGatewayProxy(Config{
		Reporter: reporter,
		CustomUpstreams: map[string]string{
			"mockstream": mockUpstream.URL,
		},
	})

	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	reqBody := `{"model":"zai-coding-plan","stream":true,"messages":[{"role":"user","content":"generate code"}]}`
	req, _ := http.NewRequest("POST", proxyServer.URL+"/proxy/mockstream/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST to streaming proxy: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	bodyStr := string(bodyBytes)

	// Must contain data: [DONE]
	if !strings.Contains(bodyStr, "[DONE]") {
		t.Errorf("expected proxy to ensure terminal [DONE] marker, got:\n%s", bodyStr)
	}

	// Verify traffic was recorded
	traffic := proxy.AllTraffic(10)
	if len(traffic) != 1 {
		t.Errorf("expected 1 traffic record, got %d", len(traffic))
	} else {
		if !traffic[0].IsStream {
			t.Errorf("expected IsStream=true, got false")
		}
	}
}

func TestResponseCache_ExactHit(t *testing.T) {
	upstreamCalls := 0
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id": "cmpl-cache-test",
			"choices": []map[string]interface{}{
				{"message": map[string]string{"role": "assistant", "content": "Cached output"}},
			},
			"usage": map[string]int{"prompt_tokens": 100, "completion_tokens": 20},
		})
	}))
	defer mockUpstream.Close()

	proxy := NewGatewayProxy(Config{
		CustomUpstreams: map[string]string{
			"cached_mock": mockUpstream.URL,
		},
	})
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	reqPayload := `{"model":"gpt-4o","messages":[{"role":"user","content":"Run static analysis on index.ts"}]}`

	// 1st request -> Cache Miss, hits upstream
	req1, _ := http.NewRequest("POST", proxyServer.URL+"/proxy/cached_mock/v1/chat/completions", strings.NewReader(reqPayload))
	req1.Header.Set("Content-Type", "application/json")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatalf("req1 failed: %v", err)
	}
	defer resp1.Body.Close()
	if resp1.Header.Get("X-WrongTrace-Cache") == "HIT" {
		t.Errorf("expected first call to be a cache miss")
	}
	if upstreamCalls != 1 {
		t.Fatalf("expected 1 upstream call, got %d", upstreamCalls)
	}

	// 2nd request -> Exact Cache Hit, upstream not called!
	req2, _ := http.NewRequest("POST", proxyServer.URL+"/proxy/cached_mock/v1/chat/completions", strings.NewReader(reqPayload))
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("req2 failed: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.Header.Get("X-WrongTrace-Cache") != "HIT" {
		t.Errorf("expected second call to return X-WrongTrace-Cache: HIT")
	}
	if upstreamCalls != 1 {
		t.Errorf("expected upstream calls to remain 1 on cache hit, got %d", upstreamCalls)
	}
}

func TestScanAndRedactSecrets(t *testing.T) {
	leakedPrompt := `Here is my configuration:
AWS_KEY=AKIAIOSFODNN7EXAMPLE
OPENAI_KEY=sk-abcdef12345678901234567890
DB_URL=postgres://admin:SuperSecretPassword123@db.example.com:5432/prod
`
	sanitized, count := ScanAndRedactSecrets([]byte(leakedPrompt))
	if count < 3 {
		t.Errorf("expected at least 3 redacted secrets, got %d", count)
	}
	resStr := string(sanitized)
	if strings.Contains(resStr, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("AWS key not redacted: %s", resStr)
	}
	if strings.Contains(resStr, "SuperSecretPassword123") {
		t.Errorf("DB password not redacted: %s", resStr)
	}
}

func TestQuotaLimiter_BudgetExceeded(t *testing.T) {
	q := NewQuotaLimiter()
	q.SetBudget("test-project", 5.00) // $5 daily budget

	// 1. Spend $3 -> Allowed
	allowed, remaining, _ := q.CheckAndRecordSpend("test-project", 3.00)
	if !allowed || remaining != 2.00 {
		t.Errorf("expected $3 spend allowed, got allowed=%v, remaining=%f", allowed, remaining)
	}

	// 2. Spend $3 more ($6 total > $5 budget) -> Blocked
	allowed2, _, warn := q.CheckAndRecordSpend("test-project", 3.00)
	if allowed2 {
		t.Errorf("expected spend exceeding budget to be blocked")
	}
	if !strings.Contains(warn, "budget of $5.00 exceeded") {
		t.Errorf("unexpected quota warning: %s", warn)
	}
}

func TestGatewayProxy_SessionGrouping(t *testing.T) {
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-test",
			"choices": [{"message": {"role": "assistant", "content": "Hello!"}}],
			"usage": {"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150}
		}`))
	}))
	defer mockUpstream.Close()

	reporter := &fakeReporter{}
	p := NewGatewayProxy(Config{
		Reporter: reporter,
		CustomUpstreams: map[string]string{
			"testprov": mockUpstream.URL,
		},
	})

	// Make 2 consecutive requests from same client without explicit session header
	prompts := []string{`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`, `{"model":"gpt-4o","messages":[{"role":"user","content":"how are you"}]}`}
	for i, body := range prompts {
		req := httptest.NewRequest(http.MethodPost, "/proxy/testprov/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("X-Agent-Name", "TestAgent")
		req.RemoteAddr = "127.0.0.1:54321"
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d failed with code %d", i, rec.Code)
		}
	}

	if len(reporter.reports) != 2 {
		t.Fatalf("expected 2 reported events, got %d", len(reporter.reports))
	}

	// Both reports must share the same RunID (session grouping)
	if reporter.reports[0].RunID != reporter.reports[1].RunID {
		t.Errorf("expected same RunID for session, got %s and %s", reporter.reports[0].RunID, reporter.reports[1].RunID)
	}

	// Tokens must accumulate across the session
	if reporter.reports[0].PromptTokens != 100 || reporter.reports[0].CompletionTokens != 50 {
		t.Errorf("first report tokens = (%d, %d), want (100, 50)", reporter.reports[0].PromptTokens, reporter.reports[0].CompletionTokens)
	}
	if reporter.reports[1].PromptTokens != 200 || reporter.reports[1].CompletionTokens != 100 {
		t.Errorf("second report tokens = (%d, %d), want (200, 100)", reporter.reports[1].PromptTokens, reporter.reports[1].CompletionTokens)
	}
}

func TestGatewayProxy_ExplicitSessionHeader(t *testing.T) {
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-test-2",
			"choices": [{"message": {"role": "assistant", "content": "Hello explicit!"}}],
			"usage": {"prompt_tokens": 120, "completion_tokens": 60, "total_tokens": 180}
		}`))
	}))
	defer mockUpstream.Close()

	reporter := &fakeReporter{}
	p := NewGatewayProxy(Config{
		Reporter: reporter,
		CustomUpstreams: map[string]string{
			"testprov": mockUpstream.URL,
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/proxy/testprov/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("X-Session-ID", "explicit-sess-99")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("request failed with code %d", rec.Code)
	}
	if len(reporter.reports) != 1 {
		t.Fatalf("expected 1 report, got %d", len(reporter.reports))
	}
	if reporter.reports[0].RunID != "explicit-sess-99" {
		t.Errorf("expected RunID explicit-sess-99, got %s", reporter.reports[0].RunID)
	}
}

// TestGatewayProxy_ModelCatalogCallIsRelayedNotTraced is the regression for
// the live-traffic misinterpretation: GET /v1/models (and /v1/models/<id>)
// are catalog/metadata calls, not model requests. They must be relayed to the
// upstream transparently — body passed through, auth forwarded — and must
// NEVER produce a telemetry run, a traffic record, or a cached response.
func TestGatewayProxy_ModelCatalogCallIsRelayedNotTraced(t *testing.T) {
	authValue := "Bearer " + fakeTok("catalog-", "authz-", "tok-9")

	var upstreamPath string
	var upstreamAuth string
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.Path
		upstreamAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-4o"},{"id":"gpt-4o-mini"}]}`))
	}))
	defer mockUpstream.Close()

	reporter := &fakeReporter{}
	p := NewGatewayProxy(Config{Reporter: reporter})

	// Mounted at /v1 like server.go does (r.Handle("/v1/*", h.Proxy)).
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("X-Target-Upstream", mockUpstream.URL+"/v1")
	req.Header.Set("Authorization", authValue)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"gpt-4o"`) {
		t.Errorf("catalog body not relayed to client: %s", rec.Body.String())
	}

	// The upstream must see the real catalog path on its /v1 base.
	if upstreamPath != "/v1/models" {
		t.Errorf("upstream path = %q, want /v1/models (catalog path must be relayed as-is)", upstreamPath)
	}
	// Client credentials still flow through on the untraced relay.
	if upstreamAuth != authValue {
		t.Errorf("Authorization header not forwarded on catalog relay: %q", upstreamAuth)
	}

	// Nothing may be traced: no run, no traffic record.
	if len(reporter.reports) != 0 {
		t.Errorf("catalog call must not produce telemetry, got %d report(s)", len(reporter.reports))
	}
	if traffic := p.AllTraffic(0); len(traffic) != 0 {
		t.Errorf("catalog call must not produce a traffic record, got %d", len(traffic))
	}

	// A following catalog call must reach the upstream again (never served
	// from the response cache — catalog lists change server-side).
	req2 := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req2.Header.Set("X-Target-Upstream", mockUpstream.URL+"/v1")
	rec2 := httptest.NewRecorder()
	p.ServeHTTP(rec2, req2)
	if rec2.Header().Get("X-WrongTrace-Cache") == "HIT" {
		t.Errorf("catalog response must never be cached")
	}
}

// TestGatewayProxy_ModelCatalogDetailAndGeminiInferenceDistinguished: the
// detail form /v1/models/<id> is a catalog call, while Gemini-style inference
// routed through the path (models/<model>:generateContent) is NOT — its ":"
// method suffix marks a real model request that must stay traced.
func TestGatewayProxy_ModelCatalogDetailAndGeminiInferenceDistinguished(t *testing.T) {
	var hits int
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gemini-2.0-flash"}]}`))
	}))
	defer mockUpstream.Close()

	reporter := &fakeReporter{}
	p := NewGatewayProxy(Config{Reporter: reporter})

	// Catalog detail: /v1/models/gemini-2.0-flash → untraced relay.
	req := httptest.NewRequest(http.MethodGet, "/v1/models/gemini-2.0-flash", nil)
	req.Header.Set("X-Target-Upstream", mockUpstream.URL+"/v1")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("catalog detail status = %d, want 200", rec.Code)
	}
	if len(reporter.reports) != 0 || len(p.AllTraffic(0)) != 0 {
		t.Errorf("catalog detail must stay untraced (reports=%d traffic=%d)", len(reporter.reports), len(p.AllTraffic(0)))
	}

	// Gemini inference through the path: <model>:generateContent — this IS a
	// model request and must remain fully traced.
	req2 := httptest.NewRequest(http.MethodPost, "/v1/models/gemini-2.0-flash:generateContent", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	req2.Header.Set("X-Target-Upstream", mockUpstream.URL+"/v1")
	rec2 := httptest.NewRecorder()
	p.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("gemini inference status = %d, want 200", rec2.Code)
	}
	if len(reporter.reports) == 0 {
		t.Errorf("gemini-style inference through the path must stay traced (0 reports)")
	}
	if len(p.AllTraffic(0)) == 0 {
		t.Errorf("gemini-style inference must produce a traffic record")
	}
	if hits != 2 {
		t.Errorf("upstream hits = %d, want 2 (both calls must reach upstream)", hits)
	}
}

// TestIsModelCatalogPath pins the pure classifier used by the relay branch.
func TestIsModelCatalogPath(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"/v1/models", true},
		{"/models", true},
		{"v1/models", true},
		{"/v1/models/gpt-4o", true},
		{"/openai/v1/models", true},
		{"/api/tags", true},
		{"api/tags", true},
		{"/v1/chat/completions", false},
		{"/v1/completions", false},
		{"/v1/embeddings", false},
		{"/v1/messages", false},
		// Ollama native endpoints other than the tag listing stay traced.
		{"/api/chat", false},
		{"/api/generate", false},
		{"/api/show", false},
		{"/v1beta/models", true},
		// Gemini method-suffix inference must NOT be classified as catalog.
		{"/v1beta/models/gemini-2.0-flash:generateContent", false},
		{"/v1beta/models/gemini-2.0-flash:streamGenerateContent", false},
		{"", false},
		{"/", false},
	}
	for _, c := range cases {
		if got := isModelCatalogPath(c.in); got != c.want {
			t.Errorf("isModelCatalogPath(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestGatewayProxy_OllamaTagsRelayedButChatTraced extends the catalog-relay
// regression to Ollama's native API: GET /api/tags is a tag listing (metadata,
// not a model request) and must be relayed untraced, while POST /api/chat is
// real inference and must stay fully traced.
func TestGatewayProxy_OllamaTagsRelayedButChatTraced(t *testing.T) {
	var hits int
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(`{"models":[{"name":"llama3.2:latest"},{"name":"qwen2.5:7b"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"model":"llama3","message":{"role":"assistant","content":"ok"},"prompt_eval_count":30,"eval_count":10}`))
	}))
	defer mockUpstream.Close()

	reporter := &fakeReporter{}
	p := NewGatewayProxy(Config{Reporter: reporter})

	// Ollama bases carry no /v1 suffix; clients address the gateway root.
	req := httptest.NewRequest(http.MethodGet, "/api/tags", nil)
	req.Header.Set("X-Target-Upstream", mockUpstream.URL)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("tags status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "llama3.2") {
		t.Errorf("tag list not relayed to client: %s", rec.Body.String())
	}

	// Nothing may be traced for the tag listing.
	if len(reporter.reports) != 0 {
		t.Errorf("tag listing must not produce telemetry, got %d report(s)", len(reporter.reports))
	}
	if traffic := p.AllTraffic(0); len(traffic) != 0 {
		t.Errorf("tag listing must not produce a traffic record, got %d", len(traffic))
	}

	// Native Ollama inference stays traced end-to-end.
	req2 := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"model":"llama3","messages":[{"role":"user","content":"hi"}]}`))
	req2.Header.Set("X-Target-Upstream", mockUpstream.URL)
	rec2 := httptest.NewRecorder()
	p.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("chat status = %d, want 200", rec2.Code)
	}
	if len(reporter.reports) == 0 {
		t.Errorf("native /api/chat inference must stay traced (0 reports)")
	}
	if len(p.AllTraffic(0)) == 0 {
		t.Errorf("native /api/chat must produce a traffic record")
	}
	if hits != 2 {
		t.Errorf("upstream hits = %d, want 2 (both calls must reach upstream)", hits)
	}
}

func TestRouteManager_MatchRoute_PathBoundary(t *testing.T) {
	rm := &RouteManager{
		routes: make(map[string]ProxyRoute),
	}
	rm.routes["route-1"] = ProxyRoute{
		ID:             "route-1",
		Name:           "zai",
		PathPrefix:     "/proxy/zai",
		TargetUpstream: "https://api.z.ai/api/coding/paas/v4",
		ProtocolType:   "openai-compatible",
		Enabled:        true,
	}

	// 1. Exact match
	r, rem := rm.MatchRoute("/proxy/zai")
	if r == nil || r.ID != "route-1" || rem != "/" {
		t.Fatalf("expected exact match on /proxy/zai, got route=%v, rem=%s", r, rem)
	}

	// 2. Subpath match
	r, rem = rm.MatchRoute("/proxy/zai/chat/completions")
	if r == nil || r.ID != "route-1" || rem != "/chat/completions" {
		t.Fatalf("expected subpath match, got route=%v, rem=%s", r, rem)
	}

	// 3. Must NOT match prefix collision like /proxy/zaix or /proxy/zai-pro
	r, rem = rm.MatchRoute("/proxy/zaix/chat/completions")
	if r != nil {
		t.Fatalf("expected NO match for /proxy/zaix, got route=%v, rem=%s", r, rem)
	}
	r, rem = rm.MatchRoute("/proxy/zai-pro/chat/completions")
	if r != nil {
		t.Fatalf("expected NO match for /proxy/zai-pro, got route=%v, rem=%s", r, rem)
	}
}

