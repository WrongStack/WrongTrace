package db

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestClampLimit(t *testing.T) {
	cases := []struct {
		name       string
		limit, def int
		want       int
	}{
		{"zero takes the default", 0, 50, 50},
		{"negative takes the default", -7, 50, 50},
		{"a sane value passes through", 25, 50, 25},
		{"the ceiling itself passes through", maxQueryLimit, 50, maxQueryLimit},
		{"above the ceiling is clamped", maxQueryLimit + 1, 50, maxQueryLimit},
		{"an absurd value is clamped", 1 << 30, 50, maxQueryLimit},
		{"a default above the ceiling is clamped too", 0, 1 << 30, maxQueryLimit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampLimit(tc.limit, tc.def); got != tc.want {
				t.Errorf("clampLimit(%d, %d) = %d, want %d", tc.limit, tc.def, got, tc.want)
			}
		})
	}
}

// TestLimitedQueries_SurviveAbsurdLimit guards the whole family at once.
//
// These queries used to enforce only the LOWER bound and leave the upper one
// to their callers -- and RecentEventsFiltered and RecentTraces preallocate
// their result slice at cap=limit BEFORE reading a single row, so a caller that
// forgot to clamp turned a query into an out-of-memory abort rather than a slow
// response. Callers did forget: three HTTP endpoints shipped without a clamp.
// The ceiling now lives at the store boundary, so every entry point inherits it.
func TestLimitedQueries_SurviveAbsurdLimit(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "limits.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	const absurd = 1 << 30

	// Each of these would allocate or scan without bound if the ceiling were
	// only enforced by callers.
	if _, err := store.RecentEvents(absurd); err != nil {
		t.Errorf("RecentEvents: %v", err)
	}
	if _, err := store.RecentFileEvents("main.go", absurd); err != nil {
		t.Errorf("RecentFileEvents: %v", err)
	}
	if _, err := store.RecentEventsFiltered(absurd, "", "", time.Time{}); err != nil {
		t.Errorf("RecentEventsFiltered: %v", err)
	}
	if _, err := store.RecentTraces(absurd); err != nil {
		t.Errorf("RecentTraces: %v", err)
	}
	if _, err := store.ProfilerHotspots(absurd); err != nil {
		t.Errorf("ProfilerHotspots: %v", err)
	}
	if _, err := store.GetRecentFileReads(absurd); err != nil {
		t.Errorf("GetRecentFileReads: %v", err)
	}
	if _, err := store.SymbolHistory("main.go", "func main()", absurd); err != nil {
		t.Errorf("SymbolHistory: %v", err)
	}
	if _, err := store.AllFileModelActivity(absurd); err != nil {
		t.Errorf("AllFileModelActivity: %v", err)
	}
	if _, err := store.ModelFrictionMatrix(absurd); err != nil {
		t.Errorf("ModelFrictionMatrix: %v", err)
	}
}

// TestRecentEvents_ClampDoesNotTruncateNormalUse: the ceiling must bound abuse
// without cutting into what the dashboard actually asks for. A clamp that also
// capped ordinary page sizes would be a silent data-loss bug.
func TestRecentEvents_ClampDoesNotTruncateNormalUse(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "clamp.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	base := time.Now().UTC().Add(-time.Hour)
	const seeded = 120
	for i := 0; i < seeded; i++ {
		if err := store.InsertEvent(EventRecord{
			EventID:    fmt.Sprintf("ev-%03d", i),
			RepoName:   "clamp-test",
			FilePath:   "main.go",
			Signature:  "func main()",
			NodeType:   "function",
			Action:     "modified",
			OccurredAt: base.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("seed event %d: %v", i, err)
		}
	}

	// A page size well under the ceiling must be honored exactly.
	got, err := store.RecentEvents(100)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if len(got) != 100 {
		t.Errorf("RecentEvents(100) returned %d rows, want 100; the clamp is cutting "+
			"into ordinary page sizes", len(got))
	}
}
