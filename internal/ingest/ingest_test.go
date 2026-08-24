package ingest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractTargetFile_And_IsFileModifyingTool(t *testing.T) {
	if !IsFileModifyingTool("write_to_file") {
		t.Error("write_to_file should be modifying tool")
	}
	if !IsFileModifyingTool("replace_file_content") {
		t.Error("replace_file_content should be modifying tool")
	}
	if !IsFileModifyingTool("custom_patch_writer") {
		t.Error("custom_patch_writer should be modifying tool")
	}
	if IsFileModifyingTool("read_file") {
		t.Error("read_file should NOT be modifying tool")
	}

	args := map[string]interface{}{
		"TargetFile": "internal/ast/diff.go",
		"Code":       "package ast",
	}
	if target := ExtractTargetFile(args); target != "internal/ast/diff.go" {
		t.Errorf("expected internal/ast/diff.go, got %s", target)
	}

	args2 := map[string]interface{}{
		"file_path": "web/src/App.tsx",
	}
	if target := ExtractTargetFile(args2); target != "web/src/App.tsx" {
		t.Errorf("expected web/src/App.tsx, got %s", target)
	}
}

func TestParseJSONLTranscript(t *testing.T) {
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "claude-session.jsonl")

	sampleJSONL := `{"type":"USER_INPUT","content":"Refactor parser","created_at":"2026-08-22T10:00:00Z"}
{"type":"PLANNER_RESPONSE","model":"claude-3-7-sonnet","tool_calls":[{"name":"replace_file_content","args":{"TargetFile":"internal/parser.go","ReplacementContent":"func Parse() {}"}}],"usage":{"input_tokens":15000,"output_tokens":1200},"created_at":"2026-08-22T10:00:05Z"}
`
	if err := os.WriteFile(logFile, []byte(sampleJSONL), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	events, err := ParseJSONLTranscript(logFile)
	if err != nil {
		t.Fatalf("ParseJSONLTranscript failed: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.ModelName != "claude-3-7-sonnet" {
		t.Errorf("expected model claude-3-7-sonnet, got %s", ev.ModelName)
	}
	if ev.TargetFile != "internal/parser.go" {
		t.Errorf("expected target file internal/parser.go, got %s", ev.TargetFile)
	}
	if ev.PromptTokens != 15000 || ev.CompletionTokens != 1200 {
		t.Errorf("tokens mismatch: in=%d out=%d", ev.PromptTokens, ev.CompletionTokens)
	}
	if ev.CostUSD <= 0 {
		t.Errorf("expected positive calculated cost, got %f", ev.CostUSD)
	}
}

func TestParseAiderHistory(t *testing.T) {
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, ".aider.chat.history.md")

	sampleAider := `# Aider chat history
Model: deepseek-r1
Tokens: 25k in, 1k out

Applied edit to src/utils.py
`
	if err := os.WriteFile(logFile, []byte(sampleAider), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	events, err := ParseAiderHistory(logFile)
	if err != nil {
		t.Fatalf("ParseAiderHistory failed: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].ModelName != "deepseek-r1" || events[0].TargetFile != "src/utils.py" {
		t.Errorf("aider event mismatch: %+v", events[0])
	}
}

func TestSessionWatcher_PollOnce(t *testing.T) {
	tempDir := t.TempDir()
	var captured []ToolCallEvent

	sw := NewSessionWatcher(func(ev ToolCallEvent) {
		captured = append(captured, ev)
	})

	sw.AddWatchDir(tempDir)
	sw.DiscoverAgentDirs(tempDir)

	logFile := filepath.Join(tempDir, "test.jsonl")
	sampleJSONL := `{"type":"PLANNER_RESPONSE","model":"gpt-4o","tool_calls":[{"name":"write_to_file","args":{"TargetFile":"main.go"}}],"usage":{"input_tokens":5000,"output_tokens":500}}`
	_ = os.WriteFile(logFile, []byte(sampleJSONL), 0644)

	sw.PollOnce()

	if len(captured) != 1 {
		t.Fatalf("expected 1 captured event from SessionWatcher, got %d", len(captured))
	}
	if captured[0].TargetFile != "main.go" || captured[0].ModelName != "gpt-4o" {
		t.Errorf("captured event mismatch: %+v", captured[0])
	}
}

func TestExtractModelFromRow_AntigravityTranscript(t *testing.T) {
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "antigravity-transcript.jsonl")

	sample := `{"type":"USER_INPUT","content":"<USER_SETTINGS_CHANGE>\nThe user changed setting ` + "`Model Selection`" + ` from None to Gemini 3.7 Flash (Medium).\n</USER_SETTINGS_CHANGE>"}
{"type":"PLANNER_RESPONSE","tool_calls":[{"name":"replace_file_content","args":{"TargetFile":"internal/db/queries.go"}}],"usage":{"input_tokens":8000,"output_tokens":400}}
`
	if err := os.WriteFile(logFile, []byte(sample), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	events, err := ParseJSONLTranscript(logFile)
	if err != nil {
		t.Fatalf("ParseJSONLTranscript failed: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].ModelName != "gemini-3.7-flash" {
		t.Errorf("expected extracted model gemini-3.7-flash, got %s", events[0].ModelName)
	}
}

func TestSessionWatcher_GlobalDiscoveryAndFileReads(t *testing.T) {
	// Test global discovery runs safely without crash on a separate instance
	swGlobal := NewSessionWatcher(nil)
	swGlobal.DiscoverGlobalAgentDirs()

	tempDir := t.TempDir()
	var toolEvents []ToolCallEvent
	var readEvents []FileReadEvent

	sw := NewSessionWatcher(func(ev ToolCallEvent) {
		toolEvents = append(toolEvents, ev)
	})
	sw.SetOnReadEvent(func(ev FileReadEvent) {
		readEvents = append(readEvents, ev)
	})

	sw.DiscoverAgentDirs("")
	sw.DiscoverAgentDirs(tempDir)

	// Create subdirectories and test detection
	claudeDir := filepath.Join(tempDir, ".claude", "logs")
	_ = os.MkdirAll(claudeDir, 0755)
	sw.DiscoverAgentDirs(tempDir)

	// Write transcript with both view_file (read) and replace_file_content (tool)
	logFile := filepath.Join(claudeDir, "session.jsonl")
	transcriptContent := `{"type":"PLANNER_RESPONSE","model":"claude-3-7-sonnet","tool_calls":[{"name":"view_file","args":{"AbsolutePath":"internal/core/engine.go","StartLine":10,"EndLine":50}}],"usage":{"input_tokens":3000,"output_tokens":200}}
{"type":"PLANNER_RESPONSE","model":"claude-3-7-sonnet","tool_calls":[{"name":"replace_file_content","args":{"TargetFile":"internal/core/engine.go","ReplacementContent":"foo"}}],"usage":{"input_tokens":4000,"output_tokens":300}}
`
	_ = os.WriteFile(logFile, []byte(transcriptContent), 0644)

	sw.PollOnce()

	if len(toolEvents) != 1 {
		t.Errorf("expected 1 tool event, got %d", len(toolEvents))
	}
	if len(readEvents) != 1 {
		t.Errorf("expected 1 read event, got %d", len(readEvents))
	}
	if len(readEvents) > 0 {
		r := readEvents[0]
		if r.FilePath != "internal/core/engine.go" || r.StartLine != 10 || r.EndLine != 50 {
			t.Errorf("unexpected read event: %+v", r)
		}
	}
}
