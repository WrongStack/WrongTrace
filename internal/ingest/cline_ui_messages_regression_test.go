package ingest

import (
	"os"
	"path/filepath"
	"testing"
)

// clineUIMessages is shaped like the ui_messages.json Cline/Roo write per task
// (cline apps/vscode/src/shared/ExtensionMessage.ts: ClineMessage,
// ClineSayTool, ClineApiReqInfo; ClineMessageModelInfo in shared/messages).
const clineUIMessages = `[
 {"ts":1712345678000,"type":"say","say":"task","text":"add a flag"},
 {"ts":1712345678100,"type":"say","say":"api_req_started","text":"{\"request\":\"...\",\"tokensIn\":1200,\"tokensOut\":300,\"cost\":0.012}","modelInfo":{"modelId":"anthropic/claude-sonnet-4","providerId":"openrouter","mode":"act"}},
 {"ts":1712345678200,"type":"say","say":"tool","text":"{\"tool\":\"readFile\",\"path\":\"src/cli.ts\"}"},
 {"ts":1712345678300,"type":"say","say":"tool","text":"{\"tool\":\"editedExistingFile\",\"path\":\"src/cli.ts\",\"diff\":\"...\"}","partial":true},
 {"ts":1712345678300,"type":"say","say":"tool","text":"{\"tool\":\"editedExistingFile\",\"path\":\"src/cli.ts\",\"diff\":\"...\"}"},
 {"ts":1712345678400,"type":"say","say":"api_req_started","text":"{\"tokensIn\":800,\"tokensOut\":100,\"cost\":0.006}"},
 {"ts":1712345678500,"type":"ask","ask":"tool","text":"{\"tool\":\"newFileCreated\",\"path\":\"src/flags.ts\",\"content\":\"...\"}"},
 {"ts":1712345678600,"type":"say","say":"command","text":"npm test"},
 {"ts":1712345678700,"type":"say","say":"completion_result","text":"done"}
]`

// Cline/Roo task files live in tasks/<taskId>/ and ui_messages.json is a JSON
// array; it was neither discovered (parent is the task id) nor parseable
// (the parser expected an object), so Cline/Roo edits never arrived.
func TestPollOnce_ClineUIMessagesInTaskDir(t *testing.T) {
	root := t.TempDir()
	task := filepath.Join(root, "tasks", "1712345678000")
	if err := os.MkdirAll(task, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(task, "ui_messages.json"), []byte(clineUIMessages), 0o600); err != nil {
		t.Fatal(err)
	}
	// The sibling history repeats the same edits and must not double them.
	if err := os.WriteFile(filepath.Join(task, "api_conversation_history.json"), []byte(`{"messages":[{"say":"tool","text":"src/cli.ts"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var got []ToolCallEvent
	sw := NewSessionWatcher(func(ev ToolCallEvent) { got = append(got, ev) })
	sw.AddWatchDir(filepath.Join(root, "tasks"))
	sw.PollOnce()
	sw.PollOnce() // unchanged file: nothing new

	if len(got) != 2 {
		t.Fatalf("events = %+v; want the edit and the file creation", got)
	}
	first, second := got[0], got[1]
	if first.ToolName != "editedExistingFile" || first.TargetFile != "src/cli.ts" ||
		second.ToolName != "newFileCreated" || second.TargetFile != "src/flags.ts" {
		t.Fatalf("events = %+v", got)
	}
	for _, ev := range got {
		if ev.ModelName != "anthropic/claude-sonnet-4" || ev.Provider != "openrouter" || ev.AgentName != "Cline/Roo" {
			t.Fatalf("attribution = %q/%q/%q", ev.ModelName, ev.Provider, ev.AgentName)
		}
		if ev.SessionID == "" {
			t.Fatalf("empty session id")
		}
	}
	// Usage is cumulative so far (each report replaces the run's totals).
	if first.PromptTokens != 1200 || first.CompletionTokens != 300 || second.PromptTokens != 2000 || second.CompletionTokens != 400 {
		t.Fatalf("usage = %d/%d then %d/%d; want 1200/300 then 2000/400",
			first.PromptTokens, first.CompletionTokens, second.PromptTokens, second.CompletionTokens)
	}
	if second.CostUSD < 0.0179 || second.CostUSD > 0.0181 {
		t.Fatalf("cumulative cost = %v; want 0.018", second.CostUSD)
	}
	if got := first.OccurredAt.UnixMilli(); got != 1712345678300 {
		t.Fatalf("occurred at %d; want the message ts", got)
	}
}
