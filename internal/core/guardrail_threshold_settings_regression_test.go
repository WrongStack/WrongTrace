package core

import (
	"fmt"
	"testing"
	"time"

	"github.com/wrongstack/wrongtrace/internal/db"
)

// The thrashing threshold and fragility cutoff saved in Settings drive the
// thrashing panel and the guardrail, and saving them takes effect without
// waiting for a cached snapshot to expire.
func TestThresholdSettingsTakeEffect(t *testing.T) {
	e, store := newTestEngine(t)
	t.Cleanup(e.Close)
	orig := e.GetSettings()
	t.Cleanup(func() {
		settingsMu.Lock()
		globalSettings.ThrashingThreshold = orig.ThrashingThreshold
		globalSettings.FragilityCutoff = orig.FragilityCutoff
		settingsMu.Unlock()
	})
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		if err := store.InsertEvent(db.EventRecord{
			EventID: fmt.Sprintf("ev-%d", i), RepoName: "test-repo", FilePath: "pkg/x.go",
			Signature: "function:pkg/x.go::Work", NodeType: "function", Action: "MODIFIED",
			BodyHash: fmt.Sprintf("h%d", i), OccurredAt: now.Add(-time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	counts := func() (int, int) {
		t.Helper()
		rows, err := e.ThrashingRows()
		if err != nil {
			t.Fatal(err)
		}
		snap, err := e.Metrics()
		if err != nil {
			t.Fatal(err)
		}
		return len(rows), len(snap.Thrashing)
	}
	if r, m := counts(); r != 1 || m != 1 { // default threshold 3; also warms the cache
		t.Fatalf("default threshold: rows=%d metrics=%d; want 1/1", r, m)
	}
	low, err := e.CheckGuardrail("pkg/x.go")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := e.UpdateSettings(AppSettings{ThrashingThreshold: 10, FragilityCutoff: low.HealthScore + 1}); err != nil {
		t.Fatal(err)
	}
	if r, m := counts(); r != 0 || m != 0 {
		t.Fatalf("threshold 10: rows=%d metrics=%d; want 0/0 (3 edits are not thrashing)", r, m)
	}
	if g, err := e.CheckGuardrail("pkg/x.go"); err != nil || g.Allowed {
		t.Fatalf("cutoff %d, health %d: allowed=%v err=%v; want flagged", low.HealthScore+1, g.HealthScore, g.Allowed, err)
	}

	if _, err := e.UpdateSettings(AppSettings{ThrashingThreshold: 3, FragilityCutoff: 1}); err != nil {
		t.Fatal(err)
	}
	if r, _ := counts(); r != 1 {
		t.Fatalf("threshold back to 3: rows=%d; want 1", r)
	}
	if g, _ := e.CheckGuardrail("pkg/x.go"); !g.Allowed && !g.IsFragile {
		t.Fatalf("cutoff 1, health %d: not allowed (%q)", g.HealthScore, g.Recommendation)
	}
}
