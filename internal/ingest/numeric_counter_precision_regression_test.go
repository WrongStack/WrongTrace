package ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTranscriptDecimalInt64CountersRemainExact(t *testing.T) {
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, tc := range []struct {
		input, output      string
		prompt, completion int64
	}{
		{"17", "9", 17, 9},
		{"9007199254740993", "9223372036854775807", 9007199254740993, 9223372036854775807},
		{"9007199254740993e0", "9223372036854775807.0", 9007199254740993, 9223372036854775807},
		{"-9007199254740993e0", "-9223372036854775808.0", -9007199254740993, -9223372036854775808},
		{"0", "0", 0, 0},
		{"17.9", "9e1", 17, 90},
		{"9223372036854775808", "1e100", 0, 0},
	} {
		t.Run(tc.input, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "transcript.jsonl")
			body := fmt.Sprintf(`{"usage":{"input_tokens":%s,"output_tokens":%s},"tool_calls":[{"name":"write_file","args":{"path":"file.go"}},{"name":"read_file","args":{"path":"file.go","start_line":10,"end_line":11}}],"created_at":"2026-01-02T03:04:05Z"}`+"\n", tc.input, tc.output)
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			mods, reads, err := ParseJSONLTranscriptFull(path)
			if err != nil || len(mods) != 1 || len(reads) != 1 {
				t.Fatalf("JSONL mods%d reads%d err%v", len(mods), len(reads), err)
			}
			if mods[0].PromptTokens != tc.prompt || mods[0].CompletionTokens != tc.completion || reads[0].PromptTokens != tc.prompt || reads[0].StartLine != 10 || reads[0].EndLine != 11 || reads[0].LinesReadCount != 2 {
				t.Fatalf("JSONL counters/lines: %+v / %+v", mods[0], reads[0])
			}
			path = filepath.Join(dir, "api_conversation_history.json")
			body = fmt.Sprintf(`{"tokensIn":%s,"tokensOut":%s,"messages":[{"say":"tool","text":"file.go","ts":%d}]}`, tc.input, tc.output, stamp.UnixMilli())
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			events, err := ParseClineTask(path)
			if err != nil || len(events) != 1 {
				t.Fatalf("Cline events%d err%v", len(events), err)
			}
			if events[0].PromptTokens != tc.prompt || events[0].CompletionTokens != tc.completion || !events[0].OccurredAt.Equal(stamp) {
				t.Fatalf("Cline counters/time: %+v", events[0])
			}
			for _, suffix := range []string{" {}", " trailing"} {
				if err := os.WriteFile(path, []byte(body+suffix), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := ParseClineTask(path); err == nil {
					t.Fatal("trailing Cline JSON accepted")
				}
			}
		})
	}
}
