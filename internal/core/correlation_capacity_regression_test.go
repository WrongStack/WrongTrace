package core

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestRegisterFileOperationAtCapacityPreservesExistingHints(t *testing.T) {
	for _, tc := range []struct {
		name       string
		paths      int
		updatePath string
		wantRun    string
	}{
		{"below-capacity-control", 2047, "filler-0.go", "run-original"},
		{"refresh-keeps-unrelated-hint", 2048, "filler-0.go", "run-original"},
		{"conflicting-refresh-stays-unattributed", 2048, "target.go", ""},
		{"new-path-still-evicts-oldest", 2048, "new-path.go", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, store, _ := newAtlasTestEngine(t)
			t.Cleanup(e.Close)
			root := t.TempDir()
			e.setWatchRoot(root)
			file := writeFixture(t, root, "target.go", "package demo\nfunc Target() {}\n")
			now := time.Now().UTC()
			e.RegisterFileOperation(file, "run-original", now.Add(-time.Second))
			for i := 0; i < tc.paths-1; i++ {
				e.RegisterFileOperation(fmt.Sprintf("filler-%d.go", i), "run-fill", now)
			}
			e.RegisterFileOperation(tc.updatePath, "run-update", now)
			e.HandleFileChange(context.Background(), file)
			rows, err := store.RecentEvents(10)
			if err != nil || len(rows) == 0 {
				t.Fatalf("expected persisted file events, got %d, %v", len(rows), err)
			}
			for _, row := range rows {
				if row.RunID != tc.wantRun {
					t.Fatalf("EXPECTED: run=%q | ACTUAL: run=%q for %s", tc.wantRun, row.RunID, row.FilePath)
				}
			}
		})
	}
}
