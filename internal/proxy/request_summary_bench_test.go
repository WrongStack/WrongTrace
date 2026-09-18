package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func benchConversation() []byte {
	type msg struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	}
	file := strings.Repeat("func handler(w http.ResponseWriter) { return }\n", 400)
	msgs := []msg{{"system", "You are a coding agent."}}
	for i := 0; i < 40; i++ {
		msgs = append(msgs, msg{"user", "please edit the handler"})
		msgs = append(msgs, msg{"assistant", []map[string]any{{"type": "tool_use", "name": "read_file", "input": map[string]any{"path": "a.go"}}}})
		msgs = append(msgs, msg{"user", []map[string]any{{"type": "tool_result", "content": file}}})
	}
	b, _ := json.Marshal(map[string]any{"model": "m", "messages": msgs})
	return b
}

// BenchmarkRequestAnalysis measures the request side of one finalize: stats
// plus the prompt-token fallback that runs when upstream sends no usage.
func BenchmarkRequestAnalysis(b *testing.B) {
	body := benchConversation()
	b.SetBytes(int64(len(body)))
	b.Run("typed", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			s := summarizeWireRequest(body)
			_ = s.estimatedTokens(len(body))
		}
	})
	b.Run("legacy", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			// Before: one map decode for stats, one for the estimate.
			legacyRequestSummary(body)
			legacyRequestSummary(body)
		}
	})
}
