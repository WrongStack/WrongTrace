package ingest

import (
	"path/filepath"
	"testing"

	"github.com/wrongstack/wrongtrace/internal/core"
	"github.com/wrongstack/wrongtrace/internal/db"
	"github.com/wrongstack/wrongtrace/internal/ipc"
)

func TestImportedReadsKeepTranscriptRunIdentity(t *testing.T) {
	for _, active := range []int{0, 1, 2} {
		t.Run(string(rune('0'+active))+"-other-active-runs", func(t *testing.T) {
			isolateHome(t)
			store, err := db.Open(filepath.Join(t.TempDir(), "reads.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			if err := store.Migrate(); err != nil {
				t.Fatal(err)
			}
			engine := core.NewEngine(core.Config{RepoName: "fixture", Store: store})
			t.Cleanup(engine.Close)
			for i := 0; i < active; i++ {
				if err := engine.ReportRun(ipc.TelemetryReport{RunID: "unrelated-" + string(rune('a'+i)), AgentName: "other-agent", ModelName: "gpt-4o"}); err != nil {
					t.Fatal(err)
				}
			}
			// The core already preserves explicit run IDs; the missing link
			// must be proven through the transcript parser and daemon callback.
			if err := engine.RecordReadEvent(db.FileReadRecord{ReadID: "control", RunID: "explicit-run", SessionID: "explicit-session", FilePath: "control.go"}); err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			path := filepath.Join(root, "session.jsonl")
			writeFile(t, path, `{"model":"gpt-4o","tool_calls":[{"name":"read_file","args":{"path":"subject.go"}}]}`+"\n")
			var parsed FileReadEvent
			watcher := NewSessionWatcher(nil)
			watcher.SetOnReadEvent(func(ev FileReadEvent) {
				parsed = ev
				// Match the daemon's read callback: pass both identities through.
				if err := engine.RecordReadEvent(db.FileReadRecord{ReadID: ev.ReadID, RunID: ev.RunID, SessionID: ev.SessionID, FilePath: ev.FilePath, ModelName: ev.ModelName}); err != nil {
					t.Fatal(err)
				}
			})
			watcher.AddWatchDir(root)
			watcher.PollOnce()
			rows, err := store.GetRecentFileReads(10)
			if err != nil || len(rows) != 2 || parsed.SessionID == "" {
				t.Fatalf("fixture setup: parsed=%+v rows=%+v err=%v", parsed, rows, err)
			}
			for _, row := range rows {
				if row.ReadID == "control" {
					if row.RunID != "explicit-run" {
						t.Fatal("control: core replaced an explicit run id")
					}
					t.Log("CONTROL EXPECTED: explicit-run | ACTUAL: explicit-run")
					continue
				}
				t.Logf("EXPECTED: imported read run=%q | ACTUAL: run=%q, session=%q", parsed.SessionID, row.RunID, row.SessionID)
				if row.RunID != parsed.SessionID {
					t.Error("imported read lost its known run identity and used active-run inference")
				}
			}
		})
	}
}

func TestReadRunIdentitySurvivesOffsetAndCheckpointResume(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	checkpoint := filepath.Join(t.TempDir(), "offsets.json")
	header := `{"model":"gpt-4o","tool_calls":[{"name":"edit_file","args":{"path":"first.go"}}]}` + "\n"
	read := `{"tool_calls":[{"name":"read_file","args":{"path":"second.go"}},{"name":"read_file","args":{"path":"third.go"}}]}` + "\n"
	writeFile(t, path, header)
	var runID string
	first := NewSessionWatcher(func(ev ToolCallEvent) { runID = ev.SessionID })
	if err := first.EnablePersistentOffsets(checkpoint); err != nil {
		t.Fatal(err)
	}
	first.AddWatchDir(root)
	first.PollOnce()
	if runID == "" {
		t.Fatal("control: edit did not supply its session id")
	}
	writeFile(t, path, header+read)
	_, offsetReads, _, err := ParseJSONLTranscriptFromOffset(path, int64(len(header)))
	if err != nil || len(offsetReads) != 2 {
		t.Fatalf("offset reads=%+v err=%v", offsetReads, err)
	}
	var resumed []FileReadEvent
	second := NewSessionWatcher(nil)
	second.SetOnReadEvent(func(ev FileReadEvent) { resumed = append(resumed, ev) })
	if err := second.EnablePersistentOffsets(checkpoint); err != nil {
		t.Fatal(err)
	}
	second.AddWatchDir(root)
	second.PollOnce()
	if len(resumed) != 2 {
		t.Fatalf("checkpoint reads=%+v", resumed)
	}
	for _, ev := range append(offsetReads, resumed...) {
		if ev.RunID != runID || ev.SessionID != runID {
			t.Fatalf("EXPECTED: stable edit/read run=%q | ACTUAL: %+v", runID, ev)
		}
	}
}
