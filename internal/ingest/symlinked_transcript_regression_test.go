package ingest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An unchanged transcript reached through a symlink emits its events once.
// The scan took size from the link itself while processFile read the target,
// so every poll looked like a truncation and replayed the whole file.
func TestPollOnce_SymlinkedTranscriptEmitsOnce(t *testing.T) {
	const line = `{"model":"fixture-model","tool_calls":[{"name":"write_file","args":{"file_path":"x.go"}}],"created_at":"2026-01-01T00:00:00Z"}` + "\n"
	root := t.TempDir()
	logs := filepath.Join(root, "logs")
	real := filepath.Join(root, "real")
	for _, d := range []string{logs, real} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	body := []byte(line + line + line)
	target := filepath.Join(real, "linked.jsonl")
	if err := os.WriteFile(target, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(logs, "linked.jsonl")); err != nil {
		t.Skipf("symlinks unavailable on this host: %v", err)
	}
	if err := os.WriteFile(filepath.Join(logs, "plain.jsonl"), body, 0o600); err != nil {
		t.Fatal(err)
	}

	counts := map[string]int{}
	sw := NewSessionWatcher(func(ev ToolCallEvent) {
		if strings.Contains(ev.SessionID, "linked") {
			counts["linked"]++
		} else {
			counts["plain"]++
		}
	})
	sw.AddWatchDir(logs)
	for i := 0; i < 4; i++ {
		sw.PollOnce() // nothing on disk changes between polls
	}
	if counts["plain"] != 3 || counts["linked"] != 3 {
		t.Fatalf("events over 4 unchanged polls: plain=%d linked=%d; want 3 each", counts["plain"], counts["linked"])
	}

	// Growth through the link is still picked up.
	f, err := os.OpenFile(target, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(line)
	_ = f.Close()
	sw.PollOnce()
	if counts["linked"] != 4 {
		t.Fatalf("appended line through symlink: linked=%d; want 4", counts["linked"])
	}
}
