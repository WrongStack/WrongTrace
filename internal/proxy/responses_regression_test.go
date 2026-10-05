package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wrongstack/wrongtrace/internal/models"
)

const responsesRequestFixture = `{"model":"gpt-4o","stream":true,"instructions":"Inspect code carefully.","input":[{"role":"user","content":[{"type":"input_text","text":"Earlier task"}]},{"type":"function_call_output","call_id":"previous","output":"Earlier result"},{"role":"user","content":[{"type":"input_text","text":"Read internal/core/atlas.go"}]}]}`

// The usage counts come from the supplied Responses capture. Prompts, IDs,
// tool arguments and output are small synthetic values, not private payloads.
const responsesSnapshotFixture = `{"object":"response","id":"resp_fixture","model":"gpt-4o","status":"completed","output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"Inspect the requested file."}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"I will read the file."}]},{"type":"function_call","id":"fc_fixture","call_id":"call_fixture","name":"read_file","arguments":"{\"path\":\"internal/core/atlas.go\"}"}],"usage":{"input_tokens":104452,"output_tokens":166,"total_tokens":104618,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":59}}}`

func responsesSSEEvent(t *testing.T, event map[string]any) string {
	t.Helper()
	b, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("event: %s\r\ndata: %s\r\n\r\n", event["type"], b)
}

func responsesCompletedFixture(t *testing.T) string {
	t.Helper()
	return responsesSSEEvent(t, map[string]any{"type": "response.completed", "response": json.RawMessage(responsesSnapshotFixture)})
}

func assertResponsesFixture(t *testing.T, got PayloadAnalysis) {
	t.Helper()
	if got.PromptTokens != 104452 || got.CompletionTokens != 166 || got.TotalTokens != 104618 || got.CachedTokens != 0 || got.ReasoningTokens != 59 {
		t.Errorf("usage = input %d output %d total %d cached %d reasoning %d", got.PromptTokens, got.CompletionTokens, got.TotalTokens, got.CachedTokens, got.ReasoningTokens)
	}
	if got.WireID != "resp_fixture" || got.WireModel != "gpt-4o" || got.FinishReason != "completed" {
		t.Errorf("response metadata = %q %q %q", got.WireID, got.WireModel, got.FinishReason)
	}
	if got.AssistantReply != "I will read the file." || got.Reasoning != "Inspect the requested file." {
		t.Errorf("output = %q, reasoning = %q", got.AssistantReply, got.Reasoning)
	}
	if got.MessageCount != 3 || got.SystemPrompt != "Inspect code carefully." || got.UserIntent != "Read internal/core/atlas.go" {
		t.Errorf("request summary = %d %q %q", got.MessageCount, got.SystemPrompt, got.UserIntent)
	}
	if got.ToolCount != 1 || len(got.ToolCalls) != 1 {
		t.Fatalf("tool count = %d, tools = %+v", got.ToolCount, got.ToolCalls)
	}
	tool := got.ToolCalls[0]
	if tool.ID != "call_fixture" || tool.Name != "read_file" || tool.TargetFile != "internal/core/atlas.go" || tool.Arguments != `{"path":"internal/core/atlas.go"}` {
		t.Errorf("tool = %+v", tool)
	}
}

func TestResponsesAPICompletedSnapshot(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			body := responsesSnapshotFixture
			if stream {
				body = responsesCompletedFixture(t)
			}
			assertResponsesFixture(t, AnalyzeWirePayloads([]byte(responsesRequestFixture), []byte(body), stream))
		})
	}
}

func TestResponsesAPIStreamingDeltasAndFinalSnapshot(t *testing.T) {
	stream := responsesSSEEvent(t, map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_fixture", "model": "gpt-4o", "status": "in_progress"}})
	stream += responsesSSEEvent(t, map[string]any{"type": "response.output_text.delta", "output_index": 1, "content_index": 0, "delta": "I will read the file."})
	stream += responsesSSEEvent(t, map[string]any{"type": "response.reasoning_summary_text.delta", "output_index": 0, "summary_index": 0, "delta": "Inspect the requested file."})
	stream += responsesSSEEvent(t, map[string]any{"type": "response.output_item.added", "output_index": 2, "item": map[string]any{"type": "function_call", "id": "fc_fixture", "call_id": "call_fixture", "name": "read_file", "arguments": ""}})
	stream += responsesSSEEvent(t, map[string]any{"type": "response.function_call_arguments.delta", "output_index": 2, "delta": `{"path":`})
	stream += responsesSSEEvent(t, map[string]any{"type": "response.function_call_arguments.delta", "output_index": 2, "delta": `"internal/core/atlas.go"}`})
	stream += responsesSSEEvent(t, map[string]any{"type": "response.function_call_arguments.done", "output_index": 2, "arguments": `{"path":"internal/core/atlas.go"}`})
	stream += responsesCompletedFixture(t)
	assertResponsesFixture(t, AnalyzeWirePayloads([]byte(responsesRequestFixture), []byte(stream), true))
}

func TestResponsesAPICachedUsageIsPartOfInput(t *testing.T) {
	resp := `{"object":"response","id":"resp_cache","status":"completed","output":[],"usage":{"input_tokens":1000,"output_tokens":20,"total_tokens":1020,"input_tokens_details":{"cached_tokens":400},"output_tokens_details":{"reasoning_tokens":5}}}`
	for _, stream := range []bool{false, true} {
		body := resp
		if stream {
			body = responsesSSEEvent(t, map[string]any{"type": "response.completed", "response": json.RawMessage(resp)})
		}
		got := AnalyzeWirePayloads([]byte(responsesRequestFixture), []byte(body), stream)
		if got.PromptTokens != 1000 || got.CachedTokens != 400 || got.CompletionTokens != 20 || got.TotalTokens != 1020 || got.ReasoningTokens != 5 {
			t.Errorf("stream=%v: usage = %+v", stream, got)
		}
	}
}

func TestResponsesAPIGatewayTelemetry(t *testing.T) {
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(responsesSnapshotFixture), &snapshot); err != nil {
		t.Fatal(err)
	}
	// Analysis must consume the original event even when the inspector's
	// 64 KB stored copy cuts through this large final response snapshot.
	snapshot["instructions"] = strings.Repeat("large prompt ", 24000)
	body := responsesSSEEvent(t, map[string]any{"type": "response.completed", "response": snapshot})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("upstream path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Oai-Request-Id", "req_fixture")
		_, _ = w.Write([]byte(body))
	}))
	defer upstream.Close()
	proxy := NewGatewayProxy(Config{})
	defer proxy.Close()
	req := httptest.NewRequest(http.MethodPost, "/proxy/openai/v1/responses", strings.NewReader(responsesRequestFixture))
	req.Header.Set("X-Target-Upstream", upstream.URL)
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, req)
	proxy.waitFinalize()
	if w.Code != http.StatusOK || w.Body.String() != body || w.Header().Get("X-Oai-Request-Id") != "req_fixture" {
		t.Errorf("relay changed: status=%d header=%q body_equal=%v", w.Code, w.Header().Get("X-Oai-Request-Id"), w.Body.String() == body)
	}
	records := proxy.AllTraffic(1)
	if len(records) != 1 {
		t.Fatalf("records = %d", len(records))
	}
	rec := records[0]
	wantCost, _ := models.Global.CalculateCostDetailed(rec.Provider, "gpt-4o", 104452, 166, 0)
	if rec.PromptTokens != 104452 || rec.CompletionTokens != 166 || rec.ReasoningTokens != 59 || rec.TotalTokens != 104618 || rec.ToolCount != 1 || rec.CostUSD != wantCost || rec.CostUSD <= 0 {
		t.Errorf("telemetry = input %d output %d reasoning %d total %d tools %d cost %g", rec.PromptTokens, rec.CompletionTokens, rec.ReasoningTokens, rec.TotalTokens, rec.ToolCount, rec.CostUSD)
	}
	if !strings.Contains(rec.ResponseBody, "[body truncated") {
		t.Error("fixture did not exercise the inspector body cap")
	}
}

func TestResponsesAPIInputShapes(t *testing.T) {
	for _, tc := range []struct {
		body, system, intent string
		count                int
	}{
		{`{"instructions":"Brief instructions","input":"A plain user message"}`, "Brief instructions", "A plain user message", 1},
		{`{"input":[{"role":"developer","content":[{"type":"input_text","text":"Developer instructions"}]},{"role":"user","content":[{"type":"input_text","text":"First"},{"type":"input_text","text":"Second"}]}]}`, "Developer instructions", "First\nSecond", 2},
	} {
		got := AnalyzeWirePayloads([]byte(tc.body), nil, false)
		if got.SystemPrompt != tc.system || got.UserIntent != tc.intent || got.MessageCount != tc.count {
			t.Errorf("request summary = %d %q %q", got.MessageCount, got.SystemPrompt, got.UserIntent)
		}
	}
}

func TestResponsesAPIPartialStreamAndDoneEvents(t *testing.T) {
	stream := responsesSSEEvent(t, map[string]any{"type": "response.output_text.delta", "output_index": 3, "content_index": 1, "delta": "Second"})
	stream += responsesSSEEvent(t, map[string]any{"type": "response.output_text.delta", "output_index": 3, "content_index": 0, "delta": "First"})
	stream += responsesSSEEvent(t, map[string]any{"type": "response.output_text.done", "output_index": 3, "content_index": 0, "text": "First"})
	stream += responsesSSEEvent(t, map[string]any{"type": "response.reasoning_summary_text.delta", "output_index": 1, "summary_index": 0, "delta": "Thinking"})
	stream += responsesSSEEvent(t, map[string]any{"type": "response.reasoning_summary_text.done", "output_index": 1, "summary_index": 0, "text": "Thinking"})
	stream += responsesSSEEvent(t, map[string]any{"type": "response.output_item.added", "output_index": 2, "item": map[string]any{"type": "function_call", "call_id": "call_partial", "name": "read_file"}})
	stream += responsesSSEEvent(t, map[string]any{"type": "response.function_call_arguments.delta", "output_index": 2, "delta": `{"path":"partial.go"}`})
	for _, suffix := range []string{
		"",
		responsesSSEEvent(t, map[string]any{"type": "response.function_call_arguments.done", "output_index": 2, "arguments": `{"path":"partial.go"}`}),
		responsesSSEEvent(t, map[string]any{"type": "response.output_item.done", "output_index": 2, "item": map[string]any{"type": "function_call", "call_id": "call_partial", "name": "read_file", "arguments": `{"path":"partial.go"}`}}),
	} {
		got := AnalyzeWirePayloads([]byte(responsesRequestFixture), []byte(stream+suffix), true)
		if got.AssistantReply != "First\nSecond" || got.Reasoning != "Thinking" || got.ToolCount != 1 || got.FinishReason != "" || got.CompletionTokens <= 0 {
			t.Fatalf("partial stream = %+v", got)
		}
		if tool := got.ToolCalls[0]; tool.ID != "call_partial" || tool.TargetFile != "partial.go" || tool.Arguments != `{"path":"partial.go"}` {
			t.Errorf("partial tool = %+v", tool)
		}
	}
}

func TestResponsesAPICustomToolInput(t *testing.T) {
	stream := responsesSSEEvent(t, map[string]any{"type": "response.output_item.added", "output_index": 1, "item": map[string]any{"type": "custom_tool_call", "call_id": "call_custom", "name": "apply_patch", "input": ""}})
	stream += responsesSSEEvent(t, map[string]any{"type": "response.custom_tool_call_input.delta", "output_index": 1, "delta": "patch text"})
	stream += responsesSSEEvent(t, map[string]any{"type": "response.custom_tool_call_input.done", "output_index": 1, "input": "patch text"})
	got := AnalyzeWirePayloads(nil, []byte(stream), true)
	if got.ToolCount != 1 || got.ToolCalls[0].Name != "apply_patch" || got.ToolCalls[0].Arguments != "patch text" {
		t.Errorf("custom tool = %+v", got.ToolCalls)
	}
}

func TestResponsesAPIAuthoritativeZeroUsage(t *testing.T) {
	resp := `{"object":"response","id":"resp_zero","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`
	stream := responsesSSEEvent(t, map[string]any{"type": "response.incomplete", "response": json.RawMessage(resp)})
	got := AnalyzeWirePayloads([]byte(responsesRequestFixture), []byte(stream), true)
	if got.PromptTokens != 0 || got.CompletionTokens != 0 || got.TotalTokens != 0 || got.FinishReason != "max_output_tokens" {
		t.Errorf("explicit zero usage = %+v", got)
	}
	proxy := NewGatewayProxy(Config{})
	defer proxy.Close()
	proxy.finalize(finalizeJob{rec: ProxyTrafficRecord{Model: "gpt-4o"}, reqBody: []byte(responsesRequestFixture), respBytes: []byte(stream), isStream: true})
	rec := proxy.AllTraffic(1)[0]
	if rec.PromptTokens != 0 || rec.CompletionTokens != 0 || rec.CostUSD != 0 {
		t.Errorf("zero usage was estimated during finalize: input=%d output=%d cost=%g", rec.PromptTokens, rec.CompletionTokens, rec.CostUSD)
	}
}
