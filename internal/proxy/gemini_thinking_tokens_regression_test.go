package proxy

// Regression (audit F142): Gemini usageMetadata.thoughtsTokenCount (thinking tokens, billed
// as output) is never read. CompletionTokens = candidatesTokenCount only, so
// the record's completion/total tokens and cost silently drop the thinking
// share, and the record's TotalTokens contradicts the provider's own
// totalTokenCount, which extractUsage itself parsed into analysis.TotalTokens.
//
// External-format dependency: Gemini 2.5 usageMetadata carries
// promptTokenCount, candidatesTokenCount, thoughtsTokenCount, totalTokenCount
// with total = prompt + candidates + thoughts. The repo's own fixtures model
// Gemini thought parts ("thought":true) but never the thoughtsTokenCount field.

import (
	"testing"
	"time"
)

func TestGeminiThinkingTokensCounted(t *testing.T) {
	req := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	// CONTROL: no thinking -> total == prompt + candidates, nothing lost.
	ctrl := AnalyzeWirePayloads(req, []byte(`{"candidates":[{"content":{"parts":[{"text":"answer"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,"totalTokenCount":120},"modelVersion":"gemini-2.5-flash"}`), false)
	t.Logf("CONTROL: prompt=%d completion=%d total=%d (prompt+completion==total: %v)\n",
		ctrl.PromptTokens, ctrl.CompletionTokens, ctrl.TotalTokens, ctrl.PromptTokens+ctrl.CompletionTokens == ctrl.TotalTokens)

	body := []byte(`{"candidates":[{"content":{"parts":[{"text":"plan...","thought":true},{"text":"answer"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,"thoughtsTokenCount":1400,"totalTokenCount":1520},"modelVersion":"gemini-2.5-flash"}`)
	stream := []byte("data: " + string(body) + "\n\n")
	failed := false
	for _, c := range []struct {
		name   string
		resp   []byte
		stream bool
	}{{"non-stream", body, false}, {"stream(alt=sse)", stream, true}} {
		a := AnalyzeWirePayloads(req, c.resp, c.stream)
		t.Logf("%s EXPECTED: completion(output billed)=1420 (20 candidates + 1400 thoughts), total=1520\n", c.name)
		t.Logf("%s ACTUAL:   completion=%d reasoningTokens=%d analysis.total=%d prompt+completion=%d\n",
			c.name, a.CompletionTokens, a.ReasoningTokens, a.TotalTokens, a.PromptTokens+a.CompletionTokens)
		if a.CompletionTokens != 1420 {
			failed = true
		}
	}

	// End-to-end through finalize: the traffic record drops analysis.TotalTokens.
	p := NewGatewayProxy(Config{})
	defer p.Close()
	p.finalize(finalizeJob{rec: ProxyTrafficRecord{ID: "px-f142", Timestamp: time.Now(), Provider: "Gemini", Model: "unknown-model", StatusCode: 200, IncomingPath: "/v1beta/models/gemini-2.5-flash:generateContent"}, reqBody: req, respBytes: body})
	rec := p.AllTraffic(1)[0]
	t.Logf("RECORD EXPECTED: TotalTokens=1520 CompletionTokens=1420\n")
	t.Logf("RECORD ACTUAL:   TotalTokens=%d CompletionTokens=%d ReasoningTokens=%d CostUSD=%.6f model=%s\n", rec.TotalTokens, rec.CompletionTokens, rec.ReasoningTokens, rec.CostUSD, rec.Model)
	if rec.TotalTokens != 1520 {
		failed = true
	}
	if failed {
		t.Log("PROBLEM CONFIRMED")
		t.FailNow()
	}
	t.Log("PROBLEM NOT REPRODUCED")
}
