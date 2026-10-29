package db

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func openPathSpellingStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "paths.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	return s
}

// FileHealth must see edits under every spelling the sibling per-file queries
// see, also when the path has a non-ASCII capital: Go-side lowering folded
// "Über" while SQLite's LOWER does not, so the guardrail reported 100/safe.
func TestFileHealth_NonASCIIPathSpellings(t *testing.T) {
	for _, c := range []struct{ stored, caller string }{
		{"internal/Uber/x.go", "./internal/Uber/x.go"}, // control
		{"internal/Über/x.go", "./internal/Über/x.go"},
		{"D:/Projeler/Çalışma/x.go", "d:/Projeler/Çalışma/x.go"},
		{"internal/Über/x.go", "D:/repo/internal/Über/x.go"},
	} {
		s := openPathSpellingStore(t)
		var evs []EventRecord
		for i := 0; i < 5; i++ {
			evs = append(evs, EventRecord{
				EventID: fmt.Sprintf("e%d", i), RepoName: "repo", FilePath: c.stored,
				Signature: fmt.Sprintf("function:x.go::F%d", i), NodeType: "function",
				Action: "MODIFIED", OccurredAt: time.Now().UTC().Add(-time.Minute),
			})
		}
		if err := s.InsertEvents(evs); err != nil {
			t.Fatal(err)
		}
		h, err := s.FileHealth(c.caller)
		if err != nil {
			t.Fatal(err)
		}
		if h.RecentThrashingCount != 5 {
			t.Errorf("FileHealth(%q) over stored %q saw %d edits; want 5", c.caller, c.stored, h.RecentThrashingCount)
		}
	}
}

// A read stored with the agent's "./" spelling is the same file as the
// watcher's absolute path.
func TestReadStats_StoredDotSlashMatchesAbsoluteCaller(t *testing.T) {
	for _, stored := range []string{"internal/svc/x.go", "./internal/svc/x.go", `.\internal\svc\x.go`} {
		s := openPathSpellingStore(t)
		if err := s.InsertReadEvent(FileReadRecord{
			ReadID: "r1", RunID: "run-1", RepoName: "repo", FilePath: stored,
			AgentName: "agent", ModelName: "model-a", Provider: "p", ToolName: "read_file",
			StartLine: 10, EndLine: 20, ReadTime: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		}); err != nil {
			t.Fatal(err)
		}
		st, err := s.GetFileReadStats("D:/Repo/internal/svc/x.go")
		if err != nil {
			t.Fatal(err)
		}
		if st.TotalReads != 1 {
			t.Errorf("stored %q: TotalReads=%d for the absolute caller; want 1", stored, st.TotalReads)
		}
	}
}
