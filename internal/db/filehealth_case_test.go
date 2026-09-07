package db

import (
	"path/filepath"
	"testing"
	"time"
)

// TestFileHealth_MatchesCaseInsensitivePaths pins the round-41 fix: guardrail
// queries arrive with agent-supplied casing (MCP get_file_health_score /
// check_guardrail, IPC telemetry/file_health, HTTP ?path=) while stored event
// paths carry FS-native casing, so FileHealth must match case-insensitively —
// mirroring its sibling RecentEventsFiltered. Pre-fix, a casing-divergent
// query reported RecentThrashingCount=0 / HealthScore=100 for a file that
// churned within 24h, silently defeating the guardrail.
func TestFileHealth_MatchesCaseInsensitivePaths(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "guard.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Stored under FS-native Windows casing (what the watcher reports).
	fsPath := `D:\Codebox\PROJECTS\WrongTrace\internal\ast\parser.go`
	if err := st.InsertEvent(EventRecord{
		EventID:    "ev-1",
		RepoName:   "demo",
		FilePath:   fsPath,
		Signature:  "func:parser.go::Parse",
		NodeType:   "function",
		Action:     "MODIFIED",
		OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	cases := []struct {
		name string
		path string
	}{
		{"exact casing", fsPath},
		{"forward slashes, same casing", `D:/Codebox/PROJECTS/WrongTrace/internal/ast/parser.go`},
		{"lowercase backslashes", `d:\codebox\projects\wrongtrace\internal\ast\parser.go`},
		{"lowercase forward slashes", `d:/codebox/projects/wrongtrace/internal/ast/parser.go`},
	}
	for _, tc := range cases {
		h, err := st.FileHealth(tc.path)
		if err != nil {
			t.Fatalf("%s: FileHealth: %v", tc.name, err)
		}
		// 1 edit in 24h: penalty 8, no fragility — health 100-8 = 92.
		if h.RecentThrashingCount != 1 || h.HealthScore != 92 {
			t.Errorf("%s: count=%d health=%d, want 1/92 — the guardrail must see the churn regardless of path casing",
				tc.name, h.RecentThrashingCount, h.HealthScore)
		}
	}
}
