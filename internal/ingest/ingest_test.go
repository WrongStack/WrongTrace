package ingest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestSessionWatcher_StartPollingLifecycle(t *testing.T) {
	sw := NewSessionWatcher(nil)
	checkpoint := filepath.Join(t.TempDir(), "offsets.json")
	if err := sw.EnablePersistentOffsets(checkpoint); err != nil {
		t.Fatal(err)
	}
	sw.mu.Lock()
	sw.seenOffsets["session.jsonl"] = 42
	sw.cursorDirty = true
	sw.cursorVersion++
	sw.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	sw.StartPolling(ctx, time.Hour)
	cancel()
	deadline := time.Now().Add(time.Second)
	for !fileExists(checkpoint) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !fileExists(checkpoint) {
		t.Fatal("poller shutdown did not persist final offsets")
	}
}

func TestSessionWatcher_PrunesMissingCursorState(t *testing.T) {
	sw := NewSessionWatcher(nil)
	missing := filepath.Join(t.TempDir(), "rotated-session.jsonl")
	sw.seenOffsets[missing] = 123
	sw.seenFiles[missing] = fileState{offset: 123, modTime: time.Now()}

	sw.pruneMissingFiles(time.Now())

	if _, ok := sw.seenOffsets[missing]; ok {
		t.Fatal("missing transcript offset was retained")
	}
	if _, ok := sw.seenFiles[missing]; ok {
		t.Fatal("missing transcript state was retained")
	}
	if !sw.cursorDirty || sw.cursorVersion == 0 {
		t.Fatal("cursor checkpoint was not marked dirty after pruning")
	}
}

func TestInitialTranscriptOffsetBoundsColdStartBackfill(t *testing.T) {
	baseline := time.Now()
	size := int64(10 * 1024 * 1024)

	if got, ok := initialTranscriptOffset(size, baseline.Add(-48*time.Hour), baseline, true); !ok || got != size {
		t.Fatalf("old JSONL offset = %d, baseline=%v; want EOF %d", got, ok, size)
	}
	if got, ok := initialTranscriptOffset(size, baseline.Add(-time.Hour), baseline, true); !ok || got != size-maxInitialBackfill {
		t.Fatalf("recent JSONL offset = %d, baseline=%v; want bounded tail", got, ok)
	}
	if got, ok := initialTranscriptOffset(size, baseline.Add(-time.Hour), baseline, false); !ok || got != size {
		t.Fatalf("existing JSON offset = %d, baseline=%v; want EOF %d", got, ok, size)
	}
	if got, ok := initialTranscriptOffset(size, baseline.Add(time.Second), baseline, true); ok || got != 0 {
		t.Fatalf("new JSONL offset = %d, baseline=%v; want normal ingestion", got, ok)
	}
}

func TestIngest_DirAndFileExistsHelpers(t *testing.T) {
	tempDir := t.TempDir()
	if !dirExists(tempDir) {
		t.Errorf("expected dirExists=true for tempDir")
	}
	if fileExists(tempDir) {
		t.Errorf("expected fileExists=false for tempDir")
	}
	file := filepath.Join(tempDir, "sample.txt")
	_ = os.WriteFile(file, []byte("hello"), 0644)
	if !fileExists(file) {
		t.Errorf("expected fileExists=true for sample.txt")
	}
	if dirExists(file) {
		t.Errorf("expected dirExists=false for sample.txt")
	}
}

// TestNormalizeModelName_TrailingAnnotation is the regression guard for the
// ordering bug in normalizeModelName: models.IsJunkModel rejects any string
// containing '(', and it used to run BEFORE the "(Medium)"/"(Default)"
// annotation was stripped. That made the strip unreachable, so every annotated
// model name collapsed to "" and the transcript was attributed to
// "unknown-model" and priced on the fallback estimate.
func TestNormalizeModelName_TrailingAnnotation(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"annotated reasoning effort", "Gemini 3.7 Flash (Medium)", "gemini-3.7-flash"},
		{"annotated default tier", "Claude 3.5 Sonnet (Default)", "claude-3-5-sonnet"},
		{"unannotated still works", "Gemini 3.7 Flash", "gemini-3.7-flash"},
		// Junk must stay rejected after cleaning, not leak through the strip.
		{"leading paren is junk", "(Preview)", ""},
		{"code expression with annotation", "this.model (x)", ""},
		{"placeholder", "None", ""},
		{"empty", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeModelName(tc.in); got != tc.want {
				t.Errorf("normalizeModelName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestParseJSONLTranscript_AnnotatedModelName drives the same defect through
// the real production entry point, so the guard fails on behaviour and not on
// a helper someone could rename.
func TestParseJSONLTranscript_AnnotatedModelName(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "antigravity-transcript.jsonl")
	sample := `{"type":"PLANNER_RESPONSE","model":"Gemini 3.7 Flash (Medium)","tool_calls":[{"name":"replace_file_content","args":{"TargetFile":"internal/db/queries.go"}}],"usage":{"input_tokens":8000,"output_tokens":400}}
`
	if err := os.WriteFile(logFile, []byte(sample), 0644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	events, err := ParseJSONLTranscript(logFile)
	if err != nil {
		t.Fatalf("ParseJSONLTranscript: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].ModelName != "gemini-3.7-flash" {
		t.Errorf("annotated model name lost: got %q, want %q", events[0].ModelName, "gemini-3.7-flash")
	}
	// The annotation must not change attribution of the edit itself.
	if events[0].TargetFile != "internal/db/queries.go" {
		t.Errorf("unexpected target file: %s", events[0].TargetFile)
	}
}

// TestIsFileToolNameMatching_TokenBoundaries guards the fuzzy-matching defect: the
// stems used to be tested with strings.Contains, so a 3-4 letter fragment anywhere
// inside an unrelated word classified the tool as file I/O. That injected bogus
// path->run attribution hints (server.go RegisterFileOperation) and phantom
// FileReadEvent rows. It does NOT affect FileHealth, which counts code_node_events.
func TestIsFileToolNameMatching_TokenBoundaries(t *testing.T) {
	// Real tool names whose stem is not a separate token: covered by the explicit
	// tables rather than by widening the stems back to raw substrings.
	if !IsFileModifyingTool("NotebookEdit") || !IsFileModifyingTool("MultiEdit") {
		t.Error("suffix-shaped modifying tools must stay recognised")
	}
	if !IsFileReadingTool("NotebookRead") {
		t.Error("suffix-shaped reading tool must stay recognised")
	}

	cases := []struct {
		name      string
		predicate func(string) bool
		want      bool
	}{
		// Fragments buried inside unrelated words -- must not classify.
		{"locate_file", IsFileReadingTool, false},
		{"duplicate_file", IsFileReadingTool, false},
		{"catalog_search", IsFileReadingTool, false},
		{"thread_analyzer", IsFileReadingTool, false},
		{"misread_counter", IsFileReadingTool, false},
		{"credit_card_scan", IsFileModifyingTool, false},
		{"accredited_review", IsFileModifyingTool, false},
		// Legitimate derivations -- must keep classifying.
		{"reader_tool", IsFileReadingTool, true},
		{"inspector_view", IsFileReadingTool, true},
		{"cat_file", IsFileReadingTool, true},
		{"code_edit", IsFileModifyingTool, true},
		{"overwrite_file", IsFileModifyingTool, true},
		{"search_and_replace", IsFileModifyingTool, true},
		{"patch_applier", IsFileModifyingTool, true},
		// Cross-class guard: a read tool is never a write.
		{"read_file", IsFileModifyingTool, false},
	}

	for _, tc := range cases {
		if got := tc.predicate(tc.name); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestParseJSONLTranscript_NonFileToolsProduceNoEvents drives the same defect
// through the real parser: a transcript whose tool calls touch no files must
// yield no modifying and no read events.
func TestParseJSONLTranscript_NonFileToolsProduceNoEvents(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "claude-transcript.jsonl")
	sample := `{"type":"PLANNER_RESPONSE","model":"gpt-4o","tool_calls":[{"name":"locate_file","args":{"path":"internal/core/engine.go"}},{"name":"credit_card_scan","args":{"path":"internal/core/engine.go"}}],"usage":{"input_tokens":10,"output_tokens":5}}
`
	if err := os.WriteFile(logFile, []byte(sample), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	modEvents, readEvents, err := ParseJSONLTranscriptFull(logFile)
	if err != nil {
		t.Fatalf("ParseJSONLTranscriptFull: %v", err)
	}
	if len(modEvents) != 0 || len(readEvents) != 0 {
		t.Fatalf("non-file tools produced %d modifying and %d read event(s), want 0/0", len(modEvents), len(readEvents))
	}

	// A genuine write tool in the same transcript must still be recorded, so the
	// guard cannot be satisfied by simply dropping everything.
	sample += `{"type":"PLANNER_RESPONSE","model":"gpt-4o","tool_calls":[{"name":"write_to_file","args":{"path":"internal/core/engine.go"}}],"usage":{"input_tokens":10,"output_tokens":5}}
`
	if err := os.WriteFile(logFile, []byte(sample), 0o644); err != nil {
		t.Fatalf("rewrite transcript: %v", err)
	}
	modEvents, _, err = ParseJSONLTranscriptFull(logFile)
	if err != nil {
		t.Fatalf("ParseJSONLTranscriptFull: %v", err)
	}
	if len(modEvents) != 1 || modEvents[0].TargetFile != "internal/core/engine.go" {
		t.Fatalf("expected the real write tool to still register, got %+v", modEvents)
	}
}

// TestDetectAgentFromPath_WordsContainingAgentStemsAreNotAgents guards the
// substring defect: every branch used to be a bare strings.Contains over the whole
// lowered path, so an agent key sitting INSIDE an ordinary directory word won the
// attribution, and the wrong agent was persisted in ToolCallEvent.AgentName /
// FileReadEvent.AgentName. These paths name no agent at all.
func TestDetectAgentFromPath_WordsContainingAgentStemsAreNotAgents(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{"roo inside classroom", "/home/teacher/classroom/lessons/transcript.jsonl"},
		{"zed inside customized", "/home/dev/projects/customized-theme/transcript.jsonl"},
		{"v0 inside srv01", "/srv01/app/data/transcript.jsonl"},
		{"devin inside devine", "/home/devine/notes/transcript.jsonl"},
		{"bolt inside boltzmann", "/home/dev/theory/boltzmann-brain/transcript.jsonl"},
		{"cline inside incline", "/home/dev/lessons/incline-notes/transcript.jsonl"},
		{"goose inside gooseberry", "/home/dev/theory/gooseberry/transcript.jsonl"},
		{"lovable inside unlovable", "/home/dev/books/unlovable/transcript.jsonl"},
	}

	for _, tc := range cases {
		if got := detectAgentFromPath(tc.path); got != "Coding Agent" {
			t.Errorf("%s: detectAgentFromPath(%q) = %q, want Coding Agent", tc.name, tc.path, got)
		}
	}
}

// TestIsFileToolName_AffixDerivation guards round 9: nameHasStem must accept a stem
// only when the extra letters form a real derivation, not merely because a token
// happens to start or end with a stem. Before the fix the rule was a bare length
// gate, so dispatch_job and despatch_ticket (ending in "patch"), ready_state and
// readiness_probe (starting with "read") and editable_note (starting with "edit")
// all classified as file I/O despite touching no file -- emitting phantom events and
// bogus path->run attribution hints.
func TestIsFileToolName_AffixDerivation(t *testing.T) {
	for _, n := range []string{
		"dispatch_job", "despatch_ticket", // head "dis"/"des" is not a derivation
		"editable_note", // tail "able" describes a state, not an operation
		"preview_mode",  // "pre" is excluded on purpose
		"credit_card_scan", "accredited_review", "misread_counter", "thread_analyzer",
	} {
		if IsFileModifyingTool(n) {
			t.Errorf("%q must not classify as modifying", n)
		}
	}

	for _, n := range []string{
		"ready_state", "readiness_probe", // tails "y"/"iness" are not derivations
		"preview_mode", "catalog_search", "locate_file", "duplicate_file",
	} {
		if IsFileReadingTool(n) {
			t.Errorf("%q must not classify as reading", n)
		}
	}

	// Real derivations must survive -- the recall half, and the reason the fix
	// narrows the affix instead of deleting prefix/suffix matching outright.
	for _, tc := range []struct {
		name string
		read bool
	}{
		{"reader_tool", true},  // read + "er"
		{"readers_view", true}, // read + "s"
		{"reading_mode", true}, // read + "ing"
		{"inspector_view", true},
		{"overwrite_file", false}, // over + write
		{"rewrite_buffer", false}, // re + write
		{"patcher_run", false},    // patch + "er"
		{"code_edit", false},
		{"custom_patch_writer", false},
		{"search_and_replace", false},
	} {
		if tc.read {
			if !IsFileReadingTool(tc.name) {
				t.Errorf("regression: legitimate derivation %q no longer classifies as reading", tc.name)
			}
			continue
		}
		if !IsFileModifyingTool(tc.name) {
			t.Errorf("regression: legitimate derivation %q no longer classifies as modifying", tc.name)
		}
	}
}

// TestParseJSONLTranscript_AffixPhantomEvents drives the same rule through the real
// parser, so a future widening cannot silently restore the phantom telemetry.
func TestParseJSONLTranscript_AffixPhantomEvents(t *testing.T) {
	// tool_calls sit at the row top level (analyzer.go:200), args under "args" (:226).
	const sample = `{"type":"ASSISTANT","tool_calls":[{"name":"dispatch_job","args":{"file_path":"internal/core/engine.go","job":"nightly"}}]}
{"type":"ASSISTANT","tool_calls":[{"name":"ready_state","args":{"file_path":"internal/core/engine.go"}}]}
`
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(sample), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	modEvents, readEvents, err := ParseJSONLTranscriptFull(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(modEvents) != 0 {
		names := make([]string, 0, len(modEvents))
		for _, ev := range modEvents {
			names = append(names, ev.ToolName)
		}
		t.Errorf("parser emitted %d phantom modification event(s) %v for a tool that never writes", len(modEvents), names)
	}
	if len(readEvents) != 0 {
		names := make([]string, 0, len(readEvents))
		for _, ev := range readEvents {
			names = append(names, ev.ToolName)
		}
		t.Errorf("parser emitted %d phantom read event(s) %v for a tool that never reads", len(readEvents), names)
	}

	// A genuine write in an otherwise identical transcript must still record, so the
	// zero counts above cannot come from the parser ignoring the fixture shape.
	const realWrite = `{"type":"ASSISTANT","tool_calls":[{"name":"write_to_file","args":{"file_path":"internal/core/engine.go"}}]}
`
	realPath := filepath.Join(t.TempDir(), "real.jsonl")
	if err := os.WriteFile(realPath, []byte(realWrite), 0o644); err != nil {
		t.Fatalf("write control: %v", err)
	}
	realMod, err := ParseJSONLTranscript(realPath)
	if err != nil {
		t.Fatalf("parse control: %v", err)
	}
	if len(realMod) != 1 {
		t.Fatalf("control: got %d modification event(s), want 1", len(realMod))
	}
	if realMod[0].TargetFile != "internal/core/engine.go" {
		t.Errorf("control: TargetFile = %q, want internal/core/engine.go", realMod[0].TargetFile)
	}
}

// TestDetectAgentFromPath_RealAgentDirsStillDetected is the over-tightening guard.
// A boundary rule that only accepted whole path segments would fail the last two
// rows: MiniMax ships versioned directories and ZCode's key is dotted, so digits
// and punctuation must count as boundaries alongside separators.
func TestDetectAgentFromPath_RealAgentDirsStillDetected(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/home/dev/.claude/projects/proj/transcript.jsonl", "Claude Code"},
		{"/home/dev/.roo/storage/task-1/transcript.jsonl", "Cline/Roo"},
		{"/home/dev/.config/zed/sessions/transcript.jsonl", "Zed AI"},
		{"/home/dev/.v0/logs/transcript.jsonl", "v0.dev"},
		{"/home/dev/.local/share/github-copilot/logs/transcript.jsonl", "GitHub Copilot"},
		{"/home/dev/.wrongstack/projects/wrongtrace/transcript.jsonl", "WrongStack"},
		{"/home/dev/.antigravity/history/transcript.jsonl", "Antigravity"},
		{"/home/dev/.cursor/ai-comments/transcript.jsonl", "Cursor"},
		{"/opt/models/abab6.5s/transcript.jsonl", "MiniMax Code"},
		{"/home/dev/.z.ai/history/transcript.jsonl", "ZCode"},
	}

	for _, tc := range cases {
		if got := detectAgentFromPath(tc.path); got != tc.want {
			t.Errorf("detectAgentFromPath(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// TestParseJSONLTranscript_DirectoryWordDoesNotAttributeAgent drives the same
// defect through the real parser. It asserts only "not Cline/Roo" because the OS
// temp prefix is outside the test's control; the claim under test is that a
// "classroom" directory must not file the transcript under the Roo agent.
func TestParseJSONLTranscript_DirectoryWordDoesNotAttributeAgent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "classroom", "lessons")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	logFile := filepath.Join(dir, "transcript.jsonl")
	sample := `{"type":"PLANNER_RESPONSE","model":"gpt-4o","tool_calls":[{"name":"write_to_file","args":{"path":"notes.md"}}],"usage":{"input_tokens":10,"output_tokens":5}}
`
	if err := os.WriteFile(logFile, []byte(sample), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	events, err := ParseJSONLTranscript(logFile)
	if err != nil {
		t.Fatalf("ParseJSONLTranscript: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if got := events[0].AgentName; got == "Cline/Roo" {
		t.Fatalf("transcript under a \"classroom\" directory was attributed to %q", got)
	}
}

// TestParseAiderHistory_SessionIDFollowsPath pins the round-18 fix: every
// parser must derive SessionID from the transcript path. ParseAiderHistory
// used to hardcode "aider-session", collapsing every workspace's aider
// history onto one session id; cmd/wrongtrace's ingest sink maps
// SessionID -> ReportRun RunID and db.UpsertRun's ON CONFLICT(run_id)
// DO UPDATE clause then let the last-ingested workspace erase every other
// workspace's agent_runs row (model, tokens, cost and intent included).
func TestParseAiderHistory_SessionIDFollowsPath(t *testing.T) {
	writeHistory := func(ws, model string) string {
		dir := filepath.Join(t.TempDir(), ws)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		path := filepath.Join(dir, ".aider.chat.history.md")
		body := "# Aider chat history\nModel: " + model + "\n\nApplied edit to src/main.go\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write history: %v", err)
		}
		return path
	}

	alphaPath := writeHistory("ws-alpha", "claude-3-7-sonnet")
	alpha, err := ParseAiderHistory(alphaPath)
	if err != nil || len(alpha) != 1 {
		t.Fatalf("parse ws-alpha: err=%v events=%d", err, len(alpha))
	}
	beta, err := ParseAiderHistory(writeHistory("ws-beta", "deepseek-v3"))
	if err != nil || len(beta) != 1 {
		t.Fatalf("parse ws-beta: err=%v events=%d", err, len(beta))
	}

	// Distinct workspaces must never share a run identity: the ingest sink
	// upserts agent_runs keyed by this id, so a collision is silent data loss.
	if alpha[0].SessionID == "" {
		t.Fatal("session id must not be empty")
	}
	if alpha[0].SessionID == beta[0].SessionID {
		t.Fatalf("distinct workspaces collapsed onto one session id %q", alpha[0].SessionID)
	}

	// Same convention as JSONL transcripts (sessionIDForPath): parent
	// directory + "-" + base name without extension.
	if alpha[0].SessionID != "ws-alpha-.aider.chat.history" {
		t.Errorf("ws-alpha session id = %q, want ws-alpha-.aider.chat.history", alpha[0].SessionID)
	}
	if beta[0].SessionID != "ws-beta-.aider.chat.history" {
		t.Errorf("ws-beta session id = %q, want ws-beta-.aider.chat.history", beta[0].SessionID)
	}

	// Stable across re-reads of the same file: the id keys dedup and the
	// agent_runs row, so it must not drift between polls.
	again, err := ParseAiderHistory(alphaPath)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if again[0].SessionID != alpha[0].SessionID {
		t.Errorf("session id drifted between parses of the same file: %q vs %q", alpha[0].SessionID, again[0].SessionID)
	}
}
