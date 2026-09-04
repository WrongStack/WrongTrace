package proxy

import (
	"testing"
)

// FuzzAnalyzeWirePayloads exercises the wire analyzer on arbitrary bytes.
//
// This is the daemon's widest untrusted-input surface: respBody is whatever an
// upstream LLM provider sent back -- a truncated SSE stream, a proxy error page,
// a gzip blob mislabeled as JSON, a body cut off mid-frame by a dropped
// connection. The analyzer walks it with type assertions and index arithmetic,
// and it runs on the background finalize goroutine, so a panic here is a daemon
// crash rather than one failed request.
func FuzzAnalyzeWirePayloads(f *testing.F) {
	seeds := []struct {
		req, resp string
		stream    bool
	}{
		{`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`,
			`{"choices":[{"message":{"content":"hello"}}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`, false},
		{`{"model":"claude-3","max_tokens":100}`,
			"data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"hi\"}}\n\ndata: [DONE]\n", true},
		{`{"messages":[{"role":"user","content":[{"type":"text","text":"x"}]}]}`,
			`{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}`, false},
		// Degenerate shapes the real world produces.
		{"", "", false},
		{"", "", true},
		{"{", "}", false},
		{"null", "null", false},
		{`{"messages":null}`, `{"choices":null}`, false},
		{`{"messages":[]}`, `{"choices":[{}]}`, false},
		{"data:", "data: ", true},
		{"data: {", "data: {\n\ndata: [DONE]", true},
		// Deep nesting and wrong types where objects are expected.
		{`{"messages":[[[[[1]]]]]}`, `{"usage":"not-an-object"}`, false},
		{`{"messages":[{"content":{"deep":{"deeper":[null]}}}]}`, `{"choices":[[]]}`, false},
	}
	for _, s := range seeds {
		f.Add([]byte(s.req), []byte(s.resp), s.stream)
	}

	f.Fuzz(func(t *testing.T, req, resp []byte, stream bool) {
		// The contract is simply: never panic, whatever the wire held.
		_ = AnalyzeWirePayloads(req, resp, stream)
	})
}

// FuzzEstimatePromptTokens covers the other analyzer entry point that reads a
// caller-supplied body. It runs on the request path, where a panic would take
// down the daemon just as surely.
func FuzzEstimatePromptTokens(f *testing.F) {
	for _, seed := range []string{
		`{"messages":[{"role":"user","content":"hello"}]}`,
		`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
		`{"prompt":"raw string prompt"}`,
		`{"messages":[{"content":null}]}`,
		`{"messages":"not-an-array"}`,
		`{"messages":[[]]}`,
		"", "{", "null", "[]", "0",
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		if got := EstimatePromptTokens(body); got < 0 {
			t.Fatalf("EstimatePromptTokens returned a negative estimate %d; token counts "+
				"feed cost arithmetic and must never go below zero", got)
		}
	})
}
