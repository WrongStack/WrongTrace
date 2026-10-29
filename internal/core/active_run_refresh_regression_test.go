package core

import (
	"fmt"
	"testing"
	"time"
)

func TestReportRunRefreshDoesNotEvictActiveRuns(t *testing.T) {
	for _, tc := range []struct {
		name, refresh string
		fillers       int
		wantOriginal  bool
	}{
		{"below-capacity-control", "original", 1, true},
		{"oldest-refresh-keeps-start-time", "original", 250, true},
		{"other-refresh-keeps-oldest-run", "filler-249", 250, true},
		{"new-run-still-evicts-oldest", "new-run", 250, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := newTestEngine(t)
			t.Cleanup(e.Close)
			if err := e.ReportRun(reportFor("original")); err != nil {
				t.Fatal(err)
			}
			// A long-lived run with a recent heartbeat is an ordinary reachable
			// state. Seed its history without sleeps or relying on clock ticks.
			started := time.Unix(1700000000, 0).UTC()
			e.runMu.Lock()
			original := e.activeRuns["original"]
			original.StartedAt = started
			original.LastSeen = time.Now().Add(-time.Minute)
			e.activeRuns["original"] = original
			e.runMu.Unlock()
			for i := 0; i < tc.fillers; i++ {
				if err := e.ReportRun(reportFor(fmt.Sprintf("filler-%d", i))); err != nil {
					t.Fatal(err)
				}
			}
			if err := e.ReportRun(reportFor(tc.refresh)); err != nil {
				t.Fatal(err)
			}
			got, found := activeByID(e.ActiveRuns())["original"]
			t.Logf("EXPECTED: original present=%v, original start=%s | ACTUAL: present=%v, start=%s", tc.wantOriginal, started, found, got.StartedAt)
			if found != tc.wantOriginal || (found && !got.StartedAt.Equal(started)) {
				t.Fatal("refresh evicted live run state without increasing the active-run count")
			}
		})
	}
}
