package db

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

// A larger "days" must never delete more. Huge values overflowed
// time.AddDate, wrapping the cutoff past now (or past year 9999, which sorts
// after every stored timestamp) so "keep everything" pruned every row.
func TestClearStale_HugeHorizonDeletesNothing(t *testing.T) {
	for _, days := range []int{math.MaxInt64, 4611686018427387903, 106751991167300, maxStaleDays + 1} {
		s := openClearStaleStore(t)
		deleted, err := s.ClearStale(days)
		if err != nil {
			t.Fatalf("ClearStale(%d): %v", days, err)
		}
		if deleted != 0 {
			t.Errorf("ClearStale(%d) deleted %d rows; nothing is that old", days, deleted)
		}
	}

	// Control: an ordinary horizon still prunes only the stale row, and the
	// cap itself is a working horizon.
	for _, days := range []int{30, maxStaleDays} {
		s := openClearStaleStore(t)
		deleted, err := s.ClearStale(days)
		if err != nil {
			t.Fatalf("ClearStale(%d): %v", days, err)
		}
		want := int64(1)
		if days == maxStaleDays {
			want = 0
		}
		if deleted != want {
			t.Errorf("ClearStale(%d) deleted %d rows, want %d", days, deleted, want)
		}
	}
}

// openClearStaleStore seeds one 90-day-old event and one fresh event.
func openClearStaleStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "clear-stale.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for id, at := range map[string]time.Time{"stale": now.AddDate(0, 0, -90), "fresh": now.Add(-time.Minute)} {
		if err := s.InsertEvent(EventRecord{
			EventID: id, RepoName: "r", FilePath: "a.go", Signature: "function:a.go::" + id,
			NodeType: "function", Action: "ADDED", BodyHash: id, LOC: 1, OccurredAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return s
}
