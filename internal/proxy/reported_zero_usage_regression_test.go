package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnalyzeWirePayloadsPreservesReportedZeroUsage(t *testing.T) {
	req := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"Explain this in enough detail to estimate a prompt"}]}`)
	for _, tc := range []struct {
		name, body string
		stream     bool
	}{
		{"openai", `{"choices":[{"message":{"content":"A cached response"}}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`, false},
		{"anthropic", `{"content":[{"type":"text","text":"A cached response"}],"usage":{"input_tokens":0,"output_tokens":0}}`, false},
		{"gemini", `{"candidates":[{"content":{"parts":[{"text":"A cached response"}]}}],"usageMetadata":{"promptTokenCount":0,"candidatesTokenCount":0,"totalTokenCount":0}}`, false},
		{"ollama", `{"message":{"content":"A cached response"},"prompt_eval_count":0,"eval_count":0}`, false},
		{"openai-stream", "data: {\"choices\":[{\"delta\":{\"content\":\"A cached response\"}}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":0,\"completion_tokens\":0,\"total_tokens\":0}}\n\ndata: [DONE]\n\n", true},
		{"anthropic-stream", "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":0,\"output_tokens\":1}}}\n\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"A cached response\"}}\n\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":0}}\n\n", true},
		{"gemini-stream", "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"A cached response\"}]}}],\"usageMetadata\":{\"promptTokenCount\":0,\"candidatesTokenCount\":0,\"totalTokenCount\":0}}\n\n", true},
		{"ollama-stream", "{\"message\":{\"content\":\"A cached response\"},\"prompt_eval_count\":0,\"eval_count\":0,\"done\":true}\n", true},
		{"zero-final-snapshot", "data: {\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}\n\ndata: {\"usage\":{\"prompt_tokens\":0,\"completion_tokens\":0,\"total_tokens\":0}}\n\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := AnalyzeWirePayloads(req, []byte(tc.body), tc.stream)
			if got.PromptTokens != 0 || got.CompletionTokens != 0 || got.TotalTokens != 0 {
				t.Fatalf("reported zero usage replaced by estimate: prompt/output/total = %d/%d/%d", got.PromptTokens, got.CompletionTokens, got.TotalTokens)
			}
		})
	}

	t.Run("missing-usage-still-estimated", func(t *testing.T) {
		got := AnalyzeWirePayloads(req, []byte(`{"choices":[{"message":{"content":"A reply with no reported usage"}}]}`), false)
		if got.PromptTokens <= 0 || got.CompletionTokens <= 0 {
			t.Fatalf("missing usage should be estimated: %+v", got)
		}
	})
	t.Run("cache-only-input", func(t *testing.T) {
		got := AnalyzeWirePayloads(req, []byte(`{"content":[],"usage":{"input_tokens":0,"cache_read_input_tokens":50,"cache_creation_input_tokens":10,"output_tokens":0}}`), false)
		if got.PromptTokens != 60 || got.CachedTokens != 50 || got.CompletionTokens != 0 || got.TotalTokens != 60 {
			t.Fatalf("cache input must still be counted: %+v", got)
		}
	})
	t.Run("partial-usage", func(t *testing.T) {
		got := AnalyzeWirePayloads(req, []byte(`{"choices":[{"message":{"content":"No billed output"}}],"usage":{"completion_tokens":0}}`), false)
		if got.PromptTokens <= 0 || got.CompletionTokens != 0 {
			t.Fatalf("estimate only the missing field: %+v", got)
		}
	})
}

func TestGatewayReportedZeroUsageDoesNotAddFallbackCost(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"A cached response\"}}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":0,\"completion_tokens\":0,\"total_tokens\":0}}\n\ndata: [DONE]\n\n")
	}))
	defer up.Close()
	p := NewGatewayProxy(Config{CustomUpstreams: map[string]string{"zero-usage": up.URL}})
	defer p.Close()
	req := httptest.NewRequest(http.MethodPost, "/proxy/zero-usage/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"A request that would otherwise be estimated"}]}`))
	p.ServeHTTP(httptest.NewRecorder(), req)
	p.waitFinalize()
	rows := p.AllTraffic(1)
	if len(rows) != 1 {
		t.Fatalf("traffic rows = %d, want 1", len(rows))
	}
	if rows[0].PromptTokens != 0 || rows[0].CompletionTokens != 0 || rows[0].CostUSD != 0 {
		t.Fatalf("zero-usage exchange billed: %+v", rows[0])
	}
}
