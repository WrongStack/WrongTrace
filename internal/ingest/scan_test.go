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
