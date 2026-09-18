package ingest

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// claudeAssistantLine mimics a Claude Code transcript line: the payload lives
// under "message" (a tool_use carrying a whole file) and "model" appears only
// inside it, which is enough to pass the line pre-filter.
func claudeAssistantLine() []byte {
	file := strings.Repeat("func handler(w http.ResponseWriter) { return }\n", 300)
	line := map[string]any{
		"type":      "assistant",
		"uuid":      "b6f1",
		"sessionId": "s-1",
		"message": map[string]any{
			"model": "claude-sonnet",
			"role":  "assistant",
			"content": []any{map[string]any{
				"type": "tool_use", "name": "Write",
				"input": map[string]any{"file_path": "a.go", "content": file},
			}},
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 20},
		},
	}
	b, _ := json.Marshal(line)
	return b
}

// decodeJSONLRow must hand the parser exactly the keys a full map decode
// would, for every key the parser reads.
func TestDecodeJSONLRowMatchesFullDecode(t *testing.T) {
	lines := []string{
		string(claudeAssistantLine()),
		`{"type":"USER_INPUT","content":"please fix <!-- model: gpt-5 --> it"}`,
		`{"model":"gpt-4o","usage":{"prompt_tokens":5},"tool_calls":[{"name":"write_file","args":{"path":"x.go"}}],"created_at":"2026-01-02T03:04:05Z"}`,
		`{"metadata":{"model":"m1"},"params":{"Model":"m2"},"other":[1,2,3]}`,
		`{"model":"a","model":"b"}`,
		`null`,
		`{}`,
	}
	for _, l := range lines {
		var full map[string]interface{}
		fullErr := json.Unmarshal([]byte(l), &full)
		got, err := decodeJSONLRow([]byte(l))
		if (err == nil) != (fullErr == nil) {
			t.Fatalf("%s: error mismatch: %v vs %v", l, err, fullErr)
		}
		want := map[string]interface{}{}
		for k, v := range full {
			if _, ok := jsonlRowKeys[k]; ok {
				want[k] = v
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got  %v\n want %v", l, got, want)
		}
		if extractModelFromRow(got) != extractModelFromRow(full) {
			t.Errorf("%s: model extraction diverged", l)
		}
	}
	for _, bad := range []string{`[1,2]`, `"str"`, `{"model":`} {
		if _, err := decodeJSONLRow([]byte(bad)); err == nil {
			t.Errorf("%s: expected error like a full map decode", bad)
		}
	}
}

func BenchmarkJSONLRowDecode(b *testing.B) {
	line := claudeAssistantLine()
	b.SetBytes(int64(len(line)))
	b.Run("selective", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_, _ = decodeJSONLRow(line)
		}
	})
	b.Run("full-map", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			var row map[string]interface{}
			_ = json.Unmarshal(line, &row)
		}
	})
}
