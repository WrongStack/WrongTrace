package proxy

import (
	"net/http"
	"testing"
)

// Provider-supplied thinking must survive analysis and reach traffic records;
// signatures and redacted thinking are not displayable reasoning text.
func TestAnalyzeWirePayloads_AnthropicThinking(t *testing.T) {
	for _, tc := range []struct {
		name, body, reasoning, reply string
		stream                       bool
	}{
		{
			name:      "JSON multiple blocks",
			body:      `{"content":[{"type":"thinking","thinking":"İlk 🧪. ","signature":"not-text"},{"type":"redacted_thinking","data":"hidden"},{"type":"thinking","thinking":"Then answer."},{"type":"text","text":"42"}],"usage":{"input_tokens":10,"output_tokens":20}}`,
			reasoning: "İlk 🧪. Then answer.", reply: "42",
		},
		{
			name: "SSE ordered deltas", stream: true,
			body: "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"İlk 🧪. \"}}\n\n" +
				"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"signature_delta\",\"signature\":\"not-text\"}}\n\n" +
				"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"Then answer.\"}}\n\n" +
				"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"42\"}}\n\n" +
				"data: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":10,\"output_tokens\":20}}\n\n",
			reasoning: "İlk 🧪. Then answer.", reply: "42",
		},
		{
			name:  "JSON empty and malformed thinking",
			body:  `{"content":[{"type":"thinking","thinking":""},{"type":"thinking","thinking":7},{"type":"text","text":"42"}],"usage":{"input_tokens":10,"output_tokens":20}}`,
			reply: "42",
		},
		{
			name: "SSE omitted thinking", stream: true,
			body: "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"\"}}\n\n" +
				"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"signature_delta\",\"signature\":\"not-text\"}}\n\n" +
				"data: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":10,\"output_tokens\":20}}\n\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := AnalyzeWirePayloads(nil, []byte(tc.body), tc.stream)
			if a.Reasoning != tc.reasoning || a.AssistantReply != tc.reply {
				t.Fatalf("analysis reasoning=%q reply=%q; want reasoning=%q reply=%q", a.Reasoning, a.AssistantReply, tc.reasoning, tc.reply)
			}
			if a.PromptTokens != 10 || a.CompletionTokens != 20 || a.TotalTokens != 30 {
				t.Fatalf("analysis usage = (%d, %d, %d), want (10, 20, 30)", a.PromptTokens, a.CompletionTokens, a.TotalTokens)
			}

			p := NewGatewayProxy(Config{})
			t.Cleanup(p.Close)
			p.finalize(finalizeJob{
				rec:       ProxyTrafficRecord{ID: "thinking-test", StatusCode: http.StatusOK},
				respBytes: []byte(tc.body), isStream: tc.stream,
			})
			records := p.AllTraffic(1)
			if len(records) != 1 || records[0].Reasoning != tc.reasoning || records[0].AssistantReply != tc.reply {
				t.Fatalf("thinking/text did not reach traffic record: %+v", records)
			}
		})
	}
}
