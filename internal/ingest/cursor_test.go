package ingest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPersistentOffsetsRoundTrip(t *testing.T) {
	checkpoint := filepath.Join(t.TempDir(), "state", "offsets.json")
	first := NewSessionWatcher(nil)
	if err := first.EnablePersistentOffsets(checkpoint); err != nil {
		t.Fatal(err)
	}
	first.seenOffsets[filepath.Join("logs", "session.jsonl")] = 12345
	first.cursorDirty = true
	if err := first.saveOffsets(true); err != nil {
		t.Fatal(err)
	}

	second := NewSessionWatcher(nil)
	if err := second.EnablePersistentOffsets(checkpoint); err != nil {
		t.Fatal(err)
	}
	if got := second.seenOffsets[filepath.Join("logs", "session.jsonl")]; got != 12345 {
		t.Fatalf("restored offset = %d, want 12345", got)
	}
	if info, err := os.Stat(checkpoint); err != nil || info.Size() == 0 {
		t.Fatalf("checkpoint missing or empty: info=%v err=%v", info, err)
	}
}

func TestPersistentOffsetsRejectsInvalidCheckpoint(t *testing.T) {
	checkpoint := filepath.Join(t.TempDir(), "offsets.json")
	if err := os.WriteFile(checkpoint, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	watcher := NewSessionWatcher(nil)
	if err := watcher.EnablePersistentOffsets(checkpoint); err == nil {
		t.Fatal("expected invalid checkpoint error")
	}
	watcher.seenOffsets["replacement.jsonl"] = 7
	watcher.cursorDirty = true
	if err := watcher.saveOffsets(true); err != nil {
		t.Fatalf("replace invalid checkpoint: %v", err)
	}
}
