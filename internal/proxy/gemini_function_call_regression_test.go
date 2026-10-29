package proxy

// Audit F222: Gemini generateContent / streamGenerateContent tool calls
// (candidates[].content.parts[].functionCall) are never extracted, so a Gemini
// agent's write_file/read_file calls reach OnTraffic with ToolCalls empty and
// no file operation or read is attributed. Gemini text, thoughts and usage on
// the same bodies ARE parsed (control), as are OpenAI/Anthropic tool calls.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func gemTCProxy(t *testing.T, ct, respBody string) ProxyTrafficRecord {
	t.Helper()
	for _, k := range []string{"WRONGTRACE_HOME", "HOME", "USERPROFILE", "APPDATA"} {
		t.Setenv(k, t.TempDir())
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, respBody)
	}))
	defer up.Close()
	got := make(chan ProxyTrafficRecord, 1)
	p := NewGatewayProxy(Config{CustomUpstreams: map[string]string{"gem": up.URL},
		OnTraffic: func(rec ProxyTrafficRecord) { got <- rec }})
	defer p.Close()
	srv := httptest.NewServer(p)
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/proxy/gem/v1beta/models/gemini-2.5-pro:generateContent",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"create src/main.go"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	select {
	case rec := <-got:
		return rec
	case <-time.After(10 * time.Second):
		t.Fatal("no traffic record")
	}
	return ProxyTrafficRecord{}
}

func TestProxy_GeminiFunctionCallsExtracted(t *testing.T) {
	gemTool := `{"candidates":[{"content":{"role":"model","parts":[{"text":"Creating the file."},{"functionCall":{"name":"write_file","args":{"file_path":"src/main.go","content":"package main"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,"totalTokenCount":120},"modelVersion":"gemini-2.5-pro"}`
	oaiTool := `{"id":"c1","model":"gpt-4o","choices":[{"message":{"role":"assistant","content":"Creating the file.","tool_calls":[{"id":"call_1","type":"function","function":{"name":"write_file","arguments":"{\"file_path\":\"src/main.go\",\"content\":\"package main\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":100,"completion_tokens":20}}`

	// CONTROL: OpenAI tool call extracted; Gemini reply/usage extracted.
	oa := AnalyzeWirePayloads(nil, []byte(oaiTool), false)
	gj := AnalyzeWirePayloads(nil, []byte(gemTool), false)
	t.Logf("CONTROL openai: ToolCount=%d name=%q target=%q", oa.ToolCount, firstName(oa.ToolCalls), firstTarget(oa.ToolCalls))
	t.Logf("CONTROL gemini text/usage parsed: reply=%q prompt=%d completion=%d model=%q", gj.AssistantReply, gj.PromptTokens, gj.CompletionTokens, gj.WireModel)
	if oa.ToolCount != 1 || firstTarget(oa.ToolCalls) != "src/main.go" || gj.AssistantReply != "Creating the file." || gj.CompletionTokens != 20 {
		t.Fatal("INVALID PROOF: control failed")
	}

	gs := AnalyzeWirePayloads(nil, []byte("data: "+gemTool+"\r\n\r\n"), true)
	rec := gemTCProxy(t, "application/json", gemTool)

	t.Log("EXPECTED: ToolCount=1 name=write_file target=src/main.go for Gemini non-stream, SSE stream, and the proxied record")
	t.Logf("ACTUAL:   non-stream ToolCount=%d | sse ToolCount=%d | proxied rec.ToolCount=%d rec.ToolCalls=%v (rec.CompletionTokens=%d)",
		gj.ToolCount, gs.ToolCount, rec.ToolCount, rec.ToolCalls, rec.CompletionTokens)
	if gj.ToolCount == 0 || gs.ToolCount == 0 || rec.ToolCount == 0 {
		t.Fatal("Gemini functionCall parts were not extracted as tool calls")
	}
}

func firstName(tc []ProxyToolCall) string {
	if len(tc) == 0 {
		return ""
	}
	return tc[0].Name
}

func firstTarget(tc []ProxyToolCall) string {
	if len(tc) == 0 {
		return ""
	}
	return tc[0].TargetFile
}
