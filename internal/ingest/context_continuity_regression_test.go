package ingest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type regressionContextFixture struct {
	path    string
	watcher *SessionWatcher
	mods    []ToolCallEvent
	reads   []FileReadEvent
	step    int
}

func regressionContextNew(t *testing.T, path string) *regressionContextFixture {
	t.Helper()
	f := &regressionContextFixture{path: path}
	f.watcher = NewSessionWatcher(func(ev ToolCallEvent) { f.mods = append(f.mods, ev) })
	f.watcher.SetOnReadEvent(func(ev FileReadEvent) { f.reads = append(f.reads, ev) })
	return f
}

func (f *regressionContextFixture) process(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(f.path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	f.step++
	f.watcher.processFile(f.path, kindJSONL, int64(len(body)), time.Unix(1700000000+int64(f.step), 0))
}

func regressionContextHeader(model, intent string) string {
	return fmt.Sprintf(`{"type":"USER_INPUT","model":%q,"content":%q}`, model, intent) + "\n"
}

func regressionContextTools(target string) string {
	return fmt.Sprintf(`{"tool_calls":[{"name":"edit_file","args":{"path":%q}},{"name":"read_file","args":{"path":%q}}],"created_at":"2026-01-02T03:04:05Z"}`, target, target) + "\n"
}

func regressionContextAssert(t *testing.T, f *regressionContextFixture, n int, model, intent string) {
	t.Helper()
	if len(f.mods) != n || len(f.reads) != n {
		t.Fatalf("EXPECTED: tool/read count%d | ACTUAL: %d/%d", n, len(f.mods), len(f.reads))
	}
	if n == 0 {
		return
	}
	m, r := f.mods[n-1], f.reads[n-1]
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if !m.OccurredAt.Equal(stamp) || !r.OccurredAt.Equal(stamp) {
		t.Fatal("inherited context changed explicit event timestamps")
	}
	if m.ModelName != model || m.Intent != intent || r.ModelName != model || r.Intent != intent {
		t.Fatalf("EXPECTED: model%q intent%q | ACTUAL: tool%s/%q read%s/%q", model, intent, m.ModelName, m.Intent, r.ModelName, r.Intent)
	}
}

func TestJSONLContextContinuity(t *testing.T) {
	t.Run("polls and restart", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "session.jsonl")
		f := regressionContextNew(t, path)
		header := regressionContextHeader("fixture-model", "retain intent")
		f.process(t, header)
		regressionContextAssert(t, f, 0, "", "")
		f.watcher.cursorPath = filepath.Join(t.TempDir(), "offsets.json")
		if err := f.watcher.saveOffsets(true); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(f.watcher.cursorPath)
		if err != nil {
			t.Fatal(err)
		}
		var checkpoint offsetCheckpoint
		if err = json.Unmarshal(data, &checkpoint); err != nil {
			t.Fatal(err)
		}
		if checkpoint.Version != 1 || checkpoint.Contexts[path].ModelName != "fixture-model" || checkpoint.Contexts[path].Intent != "retain intent" {
			t.Fatalf("checkpoint context lost: %+v", checkpoint)
		}
		f.process(t, header+regressionContextTools("first.go"))
		regressionContextAssert(t, f, 1, "fixture-model", "retain intent")
		f.watcher.processFile(path, kindJSONL, int64(len(header+regressionContextTools("first.go"))), time.Unix(1700000002, 0))
		regressionContextAssert(t, f, 1, "fixture-model", "retain intent")
		if err = f.watcher.saveOffsets(true); err != nil {
			t.Fatal(err)
		}
		next := regressionContextNew(t, path)
		if err = next.watcher.EnablePersistentOffsets(f.watcher.cursorPath); err != nil {
			t.Fatal(err)
		}
		next.process(t, header+regressionContextTools("first.go")+regressionContextTools("second.go"))
		regressionContextAssert(t, next, 1, "fixture-model", "retain intent")
		if next.mods[0].TargetFile != "second.go" {
			t.Fatal("historical event replayed")
		}
	})
	t.Run("public offset and legacy checkpoint", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "legacy.jsonl")
		header := "\xEF\xBB\xBF" + regressionContextHeader("fixture-model", "legacy intent")
		prefix := header + regressionContextTools("old.go")
		body := prefix + regressionContextTools("new.go")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		mods, reads, off, err := ParseJSONLTranscriptFromOffset(path, int64(len(prefix)))
		if err != nil || len(mods) != 1 || len(reads) != 1 || off != int64(len(body)) || mods[0].ModelName != "fixture-model" || mods[0].Intent != "legacy intent" || reads[0].ModelName != "fixture-model" {
			t.Fatalf("public offset context: mods%+v reads%+v off%d err%v", mods, reads, off, err)
		}
		allMods, allReads, err := ParseJSONLTranscriptFull(path)
		if err != nil || len(allMods) != 2 || allReads[1].ReadID != reads[0].ReadID {
			t.Fatal("offset recovery changed read identity or full-file behavior")
		}
		checkpoint := filepath.Join(t.TempDir(), "old-offsets.json")
		data, err := json.Marshal(offsetCheckpoint{Version: 1, Offsets: map[string]int64{path: int64(len(prefix))}})
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(checkpoint, data, 0600); err != nil {
			t.Fatal(err)
		}
		f := regressionContextNew(t, path)
		if err = f.watcher.EnablePersistentOffsets(checkpoint); err != nil {
			t.Fatal(err)
		}
		f.process(t, body)
		regressionContextAssert(t, f, 1, "fixture-model", "legacy intent")
		if f.mods[0].TargetFile != "new.go" {
			t.Fatal("legacy context recovery replayed old events")
		}
	})
	t.Run("partial metadata and tool override", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "partial.jsonl")
		f := regressionContextNew(t, path)
		header := regressionContextHeader("fixture-model", "original intent")
		partial := `{"type":"USER_INPUT","model":"fixture-second","content":"new intent"`
		f.process(t, header+partial)
		regressionContextAssert(t, f, 0, "", "")
		if f.watcher.seenOffsets[path] != int64(len(header)) || f.watcher.seenContexts[path].ModelName != "fixture-model" {
			t.Fatal("partial metadata advanced committed context")
		}
		body := header + partial + "}\n" + regressionContextTools("after.go")
		f.process(t, body)
		regressionContextAssert(t, f, 1, "fixture-second", "new intent")
		override := `{"tool_calls":[{"name":"edit_file","model":"override-model","args":{"path":"override.go"}}],"created_at":"2026-01-02T03:04:05Z"}` + "\n"
		f.process(t, body+override)
		if len(f.mods) != 2 || f.mods[1].ModelName != "override-model" {
			t.Fatal("per-tool override lost")
		}
		f.process(t, body+override+regressionContextTools("later.go"))
		if len(f.mods) != 3 || f.mods[2].ModelName != "fixture-second" || f.mods[2].Intent != "new intent" {
			t.Fatal("per-tool override contaminated inherited state")
		}
	})
	t.Run("unicode and explicit clearing", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "unicode.jsonl")
		f := regressionContextNew(t, path)
		header := regressionContextHeader("fixture-model", strings.Repeat("日", 81))
		f.process(t, header)
		f.process(t, header+regressionContextTools("unicode.go"))
		regressionContextAssert(t, f, 1, "fixture-model", strings.Repeat("日", 80)+"…")
		body := header + regressionContextTools("unicode.go") + regressionContextHeader("inherit", "")
		f.process(t, body)
		f.process(t, body+regressionContextTools("clear.go"))
		regressionContextAssert(t, f, 2, "fixture-model", "")
	})
	t.Run("replacement truncation and pruning", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "replaced.jsonl")
		f := regressionContextNew(t, path)
		body := regressionContextHeader("fixture-model", "old intent") + regressionContextTools("old.go")
		f.process(t, body)
		regressionContextAssert(t, f, 1, "fixture-model", "old intent")
		replacement := strings.Repeat("ignored line\n", 40) + regressionContextTools("replacement.go")
		if len(replacement) <= len(body) {
			t.Fatal("replacement fixture must be larger")
		}
		f.process(t, replacement)
		regressionContextAssert(t, f, 2, "unknown-model", "")
		f.process(t, regressionContextHeader("fixture-second", "truncated intent")+regressionContextTools("short.go"))
		regressionContextAssert(t, f, 3, "fixture-second", "truncated intent")
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		f.watcher.pruneMissingFiles(time.Unix(1700001000, 0))
		if _, ok := f.watcher.seenContexts[path]; ok {
			t.Fatal("deleted file retained context")
		}
		if _, ok := f.watcher.seenOffsets[path]; ok {
			t.Fatal("deleted file retained offset")
		}
	})
	t.Run("empty cursor", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "empty.jsonl")
		f := regressionContextNew(t, path)
		f.process(t, "")
		regressionContextAssert(t, f, 0, "", "")
		f.process(t, regressionContextTools("empty-start.go"))
		regressionContextAssert(t, f, 1, "unknown-model", "")
	})
	t.Run("usage remains per row", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "usage.jsonl")
		f := regressionContextNew(t, path)
		header := regressionContextHeader("fixture-model", "row context")
		first := strings.Replace(regressionContextTools("first.go"), `"tool_calls":`, `"usage":{"input_tokens":5},"tool_calls":`, 1)
		f.process(t, header+first)
		if len(f.mods) != 1 || len(f.reads) != 1 || f.mods[0].PromptTokens != 5 || f.reads[0].PromptTokens != 5 {
			t.Fatal("usage control failed")
		}
		f.process(t, header+first+regressionContextTools("second.go"))
		regressionContextAssert(t, f, 2, "fixture-model", "row context")
		if f.mods[1].PromptTokens != 0 || f.reads[1].PromptTokens != 0 {
			t.Fatal("per-row usage leaked through inherited context")
		}
	})
	if t.Failed() {
		return
	}
}
