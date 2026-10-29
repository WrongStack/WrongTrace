package ingest

import (
	"os"
	"path/filepath"
	"testing"
)

// Claude Code lines carry tool calls as message.content[] tool_use blocks
// and model/usage under message; one response may be written as one line per
// content block, each repeating its usage.
const (
	ccRead  = `{"type":"assistant","message":{"id":"msg_01","model":"claude-sonnet-4-5","content":[{"type":"text","text":"reading"}],"usage":{"input_tokens":1000,"cache_read_input_tokens":500,"output_tokens":40}},"timestamp":"2026-10-01T10:00:01.000Z"}` + "\n"
	ccRead2 = `{"type":"assistant","message":{"id":"msg_01","model":"claude-sonnet-4-5","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/w/server.go","offset":10,"limit":5}}],"usage":{"input_tokens":1000,"cache_read_input_tokens":500,"output_tokens":40}},"timestamp":"2026-10-01T10:00:01.000Z"}` + "\n"
	ccUser  = `{"type":"user","message":{"role":"user","content":[{"tool_use_id":"t1","type":"tool_result","content":"package main"}]}}` + "\n"
	ccEdit  = `{"type":"assistant","message":{"id":"msg_02","model":"claude-sonnet-4-5","content":[{"type":"tool_use","id":"t2","name":"Edit","input":{"file_path":"/w/server.go","old_string":"a","new_string":"b"}}],"usage":{"input_tokens":2000,"output_tokens":60}},"timestamp":"2026-10-01T10:00:03.000Z"}` + "\n"
	ccWrite = `{"type":"assistant","message":{"id":"msg_03","model":"claude-sonnet-4-5","content":[{"type":"tool_use","id":"t3","name":"Write","input":{"file_path":"/w/new.go","content":"package main"}}],"usage":{"input_tokens":3000,"output_tokens":100}},"timestamp":"2026-10-01T10:00:04.000Z"}` + "\n"
)

func TestParseJSONL_ClaudeCodeTranscript(t *testing.T) {
	p := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(p, []byte(ccRead+ccRead2+ccUser+ccEdit+ccWrite), 0o600); err != nil {
		t.Fatal(err)
	}
	mods, reads, err := ParseJSONLTranscriptFull(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(reads) != 1 || reads[0].FilePath != "/w/server.go" || reads[0].StartLine != 10 {
		t.Fatalf("reads = %+v; want one read of /w/server.go from line 10", reads)
	}
	// msg_01 is split over two lines: its usage is counted once, on its
	// first line, so the read on the repeated line carries none.
	if reads[0].PromptTokens != 0 {
		t.Fatalf("read prompt tokens = %d", reads[0].PromptTokens)
	}
	if len(mods) != 2 || mods[0].TargetFile != "/w/server.go" || mods[1].TargetFile != "/w/new.go" {
		t.Fatalf("mods = %+v; want Edit server.go then Write new.go", mods)
	}
	if mods[0].ModelName != "claude-sonnet-4-5" || mods[0].OccurredAt.Second() != 3 {
		t.Fatalf("edit model/time = %q %v", mods[0].ModelName, mods[0].OccurredAt)
	}
	// Session totals: prompt 1500 (incl. cache read) + 2000 + 3000, output 40+60+100.
	last := mods[1]
	if last.PromptTokens != 6500 || last.CompletionTokens != 200 || last.CostUSD <= mods[0].CostUSD {
		t.Fatalf("final edit totals = %d/%d $%.6f (first edit $%.6f); want 6500/200 and growing cost",
			last.PromptTokens, last.CompletionTokens, last.CostUSD, mods[0].CostUSD)
	}
	if mods[0].PromptTokens != 3500 || mods[0].CompletionTokens != 100 {
		t.Fatalf("first edit totals = %d/%d; want 3500/100", mods[0].PromptTokens, mods[0].CompletionTokens)
	}
}

// Totals keep summing across a resume, and a cursor saved before totals
// existed is rebuilt from the prefix instead of restarting the sum at zero.
func TestParseJSONL_SessionTotalsSurviveResume(t *testing.T) {
	p := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(p, []byte(ccRead+ccRead2+ccUser+ccEdit), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, cur, err := parseJSONLResumable(p, jsonlCursor{})
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(ccWrite); err != nil {
		t.Fatal(err)
	}
	f.Close()

	for name, c := range map[string]jsonlCursor{
		"persisted totals": cur,
		"legacy cursor":    {offset: cur.offset, context: jsonlContext{ModelName: cur.context.ModelName, Intent: cur.context.Intent}},
	} {
		mods, _, _, err := parseJSONLResumable(p, c)
		if err != nil {
			t.Fatal(err)
		}
		if len(mods) != 1 || mods[0].PromptTokens != 6500 || mods[0].CompletionTokens != 200 {
			t.Fatalf("%s: resumed edit = %+v; want one edit carrying session totals 6500/200", name, mods)
		}
	}
}
