package ingest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// isolateHome keeps every test here off the real ~/.wrongtrace.
func isolateHome(t *testing.T) {
	t.Helper()
	t.Setenv("WRONGTRACE_HOME", t.TempDir())
}

type eventSink struct {
	mu      sync.Mutex
	targets []string
	events  []ToolCallEvent
}

func (s *eventSink) add(ev ToolCallEvent) {
	s.mu.Lock()
	s.events = append(s.events, ev)
	s.targets = append(s.targets, ev.TargetFile)
	s.mu.Unlock()
}

func (s *eventSink) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.targets...)
}

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func toolLine(target string) string {
	b, _ := json.Marshal(map[string]any{
		"tool_calls": []any{map[string]any{"name": "write_file", "args": map[string]any{"path": target}}},
	})
	return string(b)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestProcessFile_ClineTaskEmitsOnlyNewMessages pins that a whole-file task
// transcript emits each tool message once. It is re-parsed in full on every
// change, and every past event used to be re-emitted each time (1 message,
// then 2 → 3 events), each stamped time.Now().
func TestProcessFile_ClineTaskEmitsOnlyNewMessages(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "tasks", "t1.json")
	writeFile(t, p, `{"messages":[{"say":"tool","text":"a.go","ts":1700000000000}]}`)

	sink := &eventSink{}
	sw := NewSessionWatcher(sink.add)
	sw.AddWatchDir(dir)
	sw.PollOnce()

	writeFile(t, p, `{"messages":[{"say":"tool","text":"a.go","ts":1700000000000},{"say":"tool","text":"b.go","ts":1700000005000}]}`)
	sw.PollOnce()
	sw.PollOnce()

	if got := sink.snapshot(); !equalStrings(got, []string{"a.go", "b.go"}) {
		t.Fatalf("cline events = %v, want [a.go b.go] (each message exactly once)", got)
	}
	if want := time.UnixMilli(1700000005000).UTC(); !sink.events[1].OccurredAt.Equal(want) {
		t.Errorf("OccurredAt = %v, want the message ts %v", sink.events[1].OccurredAt, want)
	}
}

func TestProcessFile_AiderHistoryEmitsOnlyNewEdits(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	p := filepath.Join(dir, ".aider.chat.history.md")
	writeFile(t, p, "# aider chat started at 2026-09-01 10:00:00\nModel: gpt-4o\nApplied edit to a.go\n")

	sink := &eventSink{}
	sw := NewSessionWatcher(sink.add)
	sw.AddWatchDir(dir)
	sw.PollOnce()
	writeFile(t, p, "# aider chat started at 2026-09-01 10:00:00\nModel: gpt-4o\nApplied edit to a.go\nApplied edit to b.go\n")
	sw.PollOnce()
	sw.PollOnce()

	if got := sink.snapshot(); !equalStrings(got, []string{"a.go", "b.go"}) {
		t.Fatalf("aider events = %v, want [a.go b.go]", got)
	}
}

// TestProcessFile_WholeFileBaselineAndLegacyCheckpointDoNotReplay pins the
// two ways a whole-file transcript is known without a delivered count: an
// old file baselined on a fresh install, and an entry restored from a
// checkpoint written before counts were persisted. Its first change must
// emit only what changed, not its whole history.
func TestProcessFile_WholeFileBaselineAndLegacyCheckpointDoNotReplay(t *testing.T) {
	isolateHome(t)
	one := `{"messages":[{"say":"tool","text":"a.go"}]}`
	two := `{"messages":[{"say":"tool","text":"a.go"},{"say":"tool","text":"b.go"}]}`

	t.Run("fresh-install baseline", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "tasks", "old.json")
		writeFile(t, p, one)
		old := time.Now().Add(-48 * time.Hour)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
		sink := &eventSink{}
		sw := NewSessionWatcher(sink.add)
		if err := sw.EnablePersistentOffsets(filepath.Join(t.TempDir(), "offsets.json")); err != nil {
			t.Fatal(err)
		}
		sw.AddWatchDir(dir)
		sw.PollOnce()
		writeFile(t, p, two)
		sw.PollOnce()
		if got := sink.snapshot(); !equalStrings(got, []string{"b.go"}) {
			t.Fatalf("events = %v, want only the new [b.go]", got)
		}
	})

	t.Run("legacy checkpoint without counts", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "tasks", "legacy.json")
		writeFile(t, p, one)
		checkpoint := filepath.Join(t.TempDir(), "ingest-offsets.json")
		legacy, _ := json.Marshal(map[string]any{"version": 1, "offsets": map[string]int64{p: int64(len(one))}})
		writeFile(t, checkpoint, string(legacy))

		sink := &eventSink{}
		sw := NewSessionWatcher(sink.add)
		if err := sw.EnablePersistentOffsets(checkpoint); err != nil {
			t.Fatalf("legacy checkpoint must still load: %v", err)
		}
		sw.AddWatchDir(dir)
		sw.PollOnce()
		writeFile(t, p, two)
		sw.PollOnce()
		if got := sink.snapshot(); !equalStrings(got, []string{"b.go"}) {
			t.Fatalf("events = %v, want only the new [b.go]", got)
		}
	})
}

// TestParseJSONL_UnterminatedValidTailEmitsOnce pins that a finished final
// record without a trailing newline is committed. It was re-parsed and its
// tool calls re-emitted on every poll (2 records, 4 polls → 5 events).
func TestParseJSONL_UnterminatedValidTailEmitsOnce(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "sess", "transcript.jsonl")
	writeFile(t, p, toolLine("a.go")+"\n"+toolLine("b.go"))

	sink := &eventSink{}
	sw := NewSessionWatcher(sink.add)
	sw.AddWatchDir(dir)
	for i := 0; i < 4; i++ {
		sw.PollOnce()
	}
	if got := sink.snapshot(); !equalStrings(got, []string{"a.go", "b.go"}) {
		t.Fatalf("after 4 polls events = %v, want [a.go b.go]", got)
	}

	// The writer finishes the line and appends another record.
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("\n" + toolLine("c.go") + "\n")
	_ = f.Close()
	sw.PollOnce()
	if got := sink.snapshot(); !equalStrings(got, []string{"a.go", "b.go", "c.go"}) {
		t.Fatalf("after append events = %v, want [a.go b.go c.go]", got)
	}
}

// TestParseJSONL_PartialTailIsNotCommitted keeps the original guarantee: a
// line still being written is neither committed nor lost.
func TestParseJSONL_PartialTailIsNotCommitted(t *testing.T) {
	isolateHome(t)
	p := filepath.Join(t.TempDir(), "t.jsonl")
	full := toolLine("a.go")
	writeFile(t, p, full[:len(full)-5])
	ev, _, off, err := ParseJSONLTranscriptFromOffset(p, 0)
	if err != nil || len(ev) != 0 || off != 0 {
		t.Fatalf("partial tail: events=%d offset=%d err=%v, want 0/0/nil", len(ev), off, err)
	}
	writeFile(t, p, full+"\n")
	ev, _, off, err = ParseJSONLTranscriptFromOffset(p, 0)
	if err != nil || len(ev) != 1 || off != int64(len(full)+1) {
		t.Fatalf("completed line: events=%d offset=%d err=%v", len(ev), off, err)
	}
}

// TestProcessFile_TranscriptReplacedByLargerRestartsAtZero pins the offset
// fingerprint. A transcript rewritten with different, larger content used to
// resume at the old offset — mid-line in the new content — silently dropping
// its first records.
func TestProcessFile_TranscriptReplacedByLargerRestartsAtZero(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "s", "x.jsonl")
	writeFile(t, p, toolLine("a.go")+"\n")

	sink := &eventSink{}
	sw := NewSessionWatcher(sink.add)
	checkpoint := filepath.Join(t.TempDir(), "offsets.json")
	if err := sw.EnablePersistentOffsets(checkpoint); err != nil {
		t.Fatal(err)
	}
	sw.AddWatchDir(dir)
	sw.PollOnce()

	writeFile(t, p, toolLine("bbbbbbbbbbbbbbbb.go")+"\n"+toolLine("c.go")+"\n")
	sw.PollOnce()
	if got := sink.snapshot(); !equalStrings(got, []string{"a.go", "bbbbbbbbbbbbbbbb.go", "c.go"}) {
		t.Fatalf("events = %v, want [a.go bbbbbbbbbbbbbbbb.go c.go]", got)
	}

	// The fingerprint survives a restart: a replacement made while the
	// daemon was down is detected from the checkpoint alone.
	if err := sw.saveOffsets(true); err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, toolLine("dddddddddddddddddddddddddddddddddddddddd.go")+"\n"+toolLine("e.go")+"\n")
	sink2 := &eventSink{}
	sw2 := NewSessionWatcher(sink2.add)
	if err := sw2.EnablePersistentOffsets(checkpoint); err != nil {
		t.Fatal(err)
	}
	if _, ok := sw2.seenFingerprints[p]; !ok {
		t.Fatal("fingerprint not persisted in the checkpoint")
	}
	sw2.AddWatchDir(dir)
	sw2.PollOnce()
	if got := sink2.snapshot(); !equalStrings(got, []string{"dddddddddddddddddddddddddddddddddddddddd.go", "e.go"}) {
		t.Fatalf("after restart events = %v, want the whole replacement", got)
	}

	// A genuine append keeps resuming from the offset.
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(toolLine("f.go") + "\n")
	_ = f.Close()
	sw2.PollOnce()
	if got := sink2.snapshot(); len(got) != 3 || got[2] != "f.go" {
		t.Fatalf("after append events = %v, want exactly one more (f.go)", got)
	}
}

// TestParseJSONL_UTF8BOMKeepsFirstRecord pins BOM stripping at offset 0:
// the BOM made the first record invalid JSON and it was silently dropped.
func TestParseJSONL_UTF8BOMKeepsFirstRecord(t *testing.T) {
	isolateHome(t)
	p := filepath.Join(t.TempDir(), "x.jsonl")
	writeFile(t, p, "\xEF\xBB\xBF"+toolLine("a.go")+"\r\n"+toolLine("b.go")+"\r\n")
	ev, _, _, err := ParseJSONLTranscriptFromOffset(p, 0)
	if err != nil || len(ev) != 2 || ev[0].TargetFile != "a.go" {
		t.Fatalf("events=%+v err=%v, want 2 starting with a.go", ev, err)
	}
}
