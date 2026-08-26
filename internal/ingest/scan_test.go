package ingest

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// writeLine appends one JSONL transcript row naming a tool call.
func writeLine(t *testing.T, path, sessionID, tool, target string) {
	t.Helper()
	row := `{"type":"PLANNER_RESPONSE","sessionId":"` + sessionID +
		`","model":"gpt-4o","tool_calls":[{"name":"` + tool +
		`","args":{"TargetFile":"` + target +
		`"}}],"usage":{"input_tokens":5000,"output_tokens":500}}` + "\n"
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	if _, err := f.WriteString(row); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	_ = f.Close()
}

// collector is a race-safe sink for the watcher's tool-call callback.
type collector struct {
	mu     sync.Mutex
	events []ToolCallEvent
}

func (c *collector) add(ev ToolCallEvent) {
	c.mu.Lock()
	c.events = append(c.events, ev)
	c.mu.Unlock()
}

func (c *collector) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

// TestPollOnce_DetectsAppendAfterDirectorySettles guards the walk against any
// future attempt to prune directories by mtime. Appending to a transcript does
// not move its parent directory's mtime, so a scan that trusted directory
// timestamps would go blind to exactly the live sessions it exists to follow.
func TestPollOnce_DetectsAppendAfterDirectorySettles(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(sessions, "a.jsonl")
	writeLine(t, transcript, "sess-1", "write_to_file", "one.go")

	var got collector
	sw := NewSessionWatcher(got.add)
	sw.AddWatchDir(root)

	sw.PollOnce()
	if got.len() != 1 {
		t.Fatalf("first poll: want 1 event, got %d", got.len())
	}

	// Push the directory mtime outside the settle window so the discovery pass
	// is entitled to skip it, then append. Only the tracked pass can see this.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(sessions, old, old); err != nil {
		t.Fatal(err)
	}
	writeLine(t, transcript, "sess-1", "write_to_file", "two.go")

	sw.PollOnce()
	if got.len() != 2 {
		t.Fatalf("append poll: want 2 events, got %d", got.len())
	}
}

// TestPollOnce_DiscoversNewFileInWalkedDirectory covers discovery of a
// transcript that appears in an already-walked nested directory.
func TestPollOnce_DiscoversNewFileInWalkedDirectory(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "projects", "slug")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	writeLine(t, filepath.Join(nested, "first.jsonl"), "sess-a", "write_to_file", "a.go")

	var got collector
	sw := NewSessionWatcher(got.add)
	sw.AddWatchDir(root)
	sw.PollOnce()
	if got.len() != 1 {
		t.Fatalf("seed poll: want 1 event, got %d", got.len())
	}

	writeLine(t, filepath.Join(nested, "second.jsonl"), "sess-b", "write_to_file", "b.go")
	sw.PollOnce()
	if got.len() != 2 {
		t.Fatalf("discovery poll: want 2 events, got %d", got.len())
	}
}

// TestPollOnce_ReachWrongStackSubagentDepth pins the discovery of transcripts
// nested at the depth WrongStack actually produces:
// sessions/<date>/<session>/subagents/<date>/<session>/sub.jsonl is six
// directory levels below a watched project root, and the historical bound of
// five dropped every one of them.
func TestPollOnce_ReachWrongStackSubagentDepth(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "sessions", "2026-08-26", "sess-root",
		"subagents", "2026-08-26", "sess-sub")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	writeLine(t, filepath.Join(deep, "transcript.jsonl"), "sess-sub", "write_to_file", "deep.go")

	var got collector
	sw := NewSessionWatcher(got.add)
	sw.AddWatchDir(root)

	sw.PollOnce()
	if got.len() != 1 {
		t.Fatalf("six-levels-deep transcript not ingested: want 1 event, got %d", got.len())
	}
}

// TestScanDepth_ConfigurableAndEnforced proves the bound is both overridable
// (WRONGTRACE_MAX_SCAN_DEPTH reaches construction) and still enforced: with a
// tiny limit, deeper caches stay unscanned.
func TestScanDepth_ConfigurableAndEnforced(t *testing.T) {
	t.Setenv("WRONGTRACE_MAX_SCAN_DEPTH", "2")

	root := t.TempDir()
	shallow := filepath.Join(root, "level1")
	mid := filepath.Join(shallow, "level2")
	deep := filepath.Join(mid, "level3")
	for _, d := range []string{shallow, mid, deep} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeLine(t, filepath.Join(mid, "mid.jsonl"), "m", "write_to_file", "mid.go")
	writeLine(t, filepath.Join(deep, "deep.jsonl"), "d", "write_to_file", "deep.go")

	var got collector
	sw := NewSessionWatcher(got.add)
	if sw.scanDepth != 2 {
		t.Fatalf("scanDepth = %d, want 2 from WRONGTRACE_MAX_SCAN_DEPTH", sw.scanDepth)
	}
	sw.AddWatchDir(root)
	sw.PollOnce()

	if got.len() != 1 {
		t.Fatalf("limit=2: want only the level-2 transcript ingested, got %d events", got.len())
	}

	// Raising the instance's bound picks up everything beneath.
	wide := NewSessionWatcher(got.add)
	wide.scanDepth = defaultMaxScanDepth
	wide.AddWatchDir(root)
	wide.PollOnce()
	if got.len() != 3 {
		t.Fatalf("default depth: want all three events total, got %d", got.len())
	}
}

func TestClassifyLogFile(t *testing.T) {
	cases := []struct {
		name, parent string
		want         fileKind
	}{
		{"transcript.jsonl", "slug", kindJSONL},
		{"transcript_full.jsonl", "slug", kindNone},
		{"api_conversation_history.json", "tasks", kindJSON},
		{"package.json", "slug", kindNone},
		{".aider.chat.history.md", "repo", kindAider},
		{"README.md", "repo", kindNone},
	}
	for _, tc := range cases {
		if got := classifyLogFile(tc.name, tc.parent); got != tc.want {
			t.Errorf("classifyLogFile(%q, %q) = %v, want %v", tc.name, tc.parent, got, tc.want)
		}
	}
}
