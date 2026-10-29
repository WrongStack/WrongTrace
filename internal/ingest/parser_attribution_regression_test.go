package ingest

import (
	"os"
	"path/filepath"
	"testing"
)

// A transcript row carries one usage block. Read rows are summed downstream,
// so it must be counted once however many reads the row issues.
func TestParseJSONL_ParallelReadsCountRowUsageOnce(t *testing.T) {
	p := filepath.Join(t.TempDir(), "parallel.jsonl")
	body := `{"model":"gpt-4o","usage":{"input_tokens":1000,"output_tokens":200},"tool_calls":[` +
		`{"name":"read_file","args":{"path":"a.go"}},` +
		`{"name":"read_file","args":{"path":"b.go"}},` +
		`{"name":"read_file","args":{"path":"c.go"}}]}` + "\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, reads, err := ParseJSONLTranscriptFull(p)
	if err != nil {
		t.Fatal(err)
	}
	var sum int64
	for _, r := range reads {
		sum += r.PromptTokens
	}
	if len(reads) != 3 || sum != 1000 {
		t.Fatalf("%d reads summing %d prompt tokens; want 3 reads summing 1000", len(reads), sum)
	}
}

// Each aider edit belongs to the model line most recently above it, model
// names keep their provider prefix, paths keep '-' and drive letters, and
// chat prose is not an edit.
func TestParseAiderHistory_ModelPerSessionAndAnchoredEdits(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".aider.chat.history.md")
	body := "# aider chat started at 2026-09-01 10:00:00\n" +
		"> Model: gpt-4o\n" +
		"> Applied edit to a.go\n" +
		"Updated the docs and Created nothing.\n" +
		"# aider chat started at 2026-09-02 10:00:00\n" +
		"> Main model: anthropic/claude-3-5-sonnet with diff edit format\n" +
		"> Applied edit to src/my-widget.tsx\n" +
		"> Applied edit to " + `C:\repo\app.py` + "\r\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	evs, err := ParseAiderHistory(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.go@gpt-4o", "src/my-widget.tsx@anthropic/claude-3-5-sonnet", `C:\repo\app.py@anthropic/claude-3-5-sonnet`}
	var got []string
	for _, e := range evs {
		got = append(got, e.TargetFile+"@"+e.ModelName)
	}
	if len(got) != len(want) {
		t.Fatalf("events = %q; want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events = %q; want %q", got, want)
		}
	}
}
