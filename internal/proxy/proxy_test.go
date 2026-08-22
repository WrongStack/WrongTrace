package proxy

import (
	"encoding/json"
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
}
