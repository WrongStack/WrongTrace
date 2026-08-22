package db

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestOpen_Close exercises open/migrate/close on a fresh path, double
// migration (idempotence), close-twice tolerance, and nil-store safety.
func TestOpen_Close(t *testing.T) {
	s := openTestStore(t)

	if err := s.Migrate(); err != nil {
		t.Fatalf("second Migrate must be idempotent: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second close must be tolerated: %v", err)
	}
	var nilStore *Store
	if err := nilStore.Close(); err != nil {
		t.Fatalf("nil-store close must be a no-op: %v", err)
	}
}

func TestOpen_MkdirsParentDirectory(t *testing.T) {
	nested := filepath.Join(t.TempDir(), "a", "b", "c", "wrongtrace.db")
	s, err := Open(nested)
	if err != nil {
		t.Fatalf("open with nested parents: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
}

func TestOpen_ReusesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reuse.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err := s1.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	seedRun(t, s1, RunRecord{RunID: "r1", ModelName: "m"})
	_ = s1.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	ov, err := s2.Overview()
	if err != nil {
		t.Fatalf("overview after reopen: %v", err)
	}
	if ov.TotalRuns != 1 || ov.UniqueModels != 1 {
		t.Errorf("data did not survive reopen: %+v", ov)
	}
}

func TestUpsertRun_UpdatesExistingRow(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC().Add(-time.Hour)
	seedRun(t, s, RunRecord{RunID: "dup", ModelName: "first", CostUSD: 1, CreatedAt: now})
	// Same run_id upserted: last write must win for cost/model.
	seedRun(t, s, RunRecord{RunID: "dup", ModelName: "second", CostUSD: 2})

	ov, err := s.Overview()
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if ov.TotalRuns != 1 {
		t.Errorf("TotalRuns = %d, want 1 (upsert must not duplicate)", ov.TotalRuns)
	}
	if !approx(ov.TotalCost, 2) {
		t.Errorf("TotalCost = %v, want 2 (latest write wins)", ov.TotalCost)
	}
	if ov.UniqueModels != 1 {
		t.Errorf("UniqueModels = %d, want 1", ov.UniqueModels)
	}
}

func TestOverview_EmptyAndZeroCost(t *testing.T) {
	s := openTestStore(t)
	ov, err := s.Overview()
	if err != nil {
		t.Fatalf("overview on empty store: %v", err)
	}
	if ov.TotalRuns != 0 || ov.TotalEvents != 0 || !approx(ov.TotalCost, 0) || ov.UniqueModels != 0 {
		t.Errorf("empty overview = %+v, want all zeros", ov)
	}
}

func TestRecentEvents_OrderAndLimit(t *testing.T) {
	s := openTestStore(t)
	base := time.Now().UTC().Add(-2 * time.Hour)
	for i := 0; i < 7; i++ {
		seedEvent(t, s, string(rune('a'+i)), "", "sig"+string(rune('a'+i)), "MODIFIED", base.Add(time.Duration(i)*time.Second))
	}

	got, err := s.RecentEvents(3)
	if err != nil {
		t.Fatalf("recent events: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (limit)", len(got))
	}
	if got[0].EventID != "g" || got[1].EventID != "f" || got[2].EventID != "e" {
		t.Errorf("order = %s,%s,%s — want newest first (g,f,e)", got[0].EventID, got[1].EventID, got[2].EventID)
	}

	all, err := s.RecentEvents(0) // 0 -> default 50
	if err != nil {
		t.Fatalf("recent events default limit: %v", err)
	}
	if len(all) != 7 {
		t.Errorf("default limit returned %d events, want all 7", len(all))
	}
}

func TestThrashing_WindowAndDefaults(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC()

	// 4 edits inside a 1h span -> thrashing row.
	for i := 0; i < 4; i++ {
		seedEvent(t, s, "t"+string(rune('0'+i)), "run-1", "function:x.go::Hot", "MODIFIED", now.Add(-time.Duration(i)*15*time.Minute))
	}
	// 4 edits spread over 3 days -> edit_count passes but window > 24h, must be excluded.
	for i := 0; i < 4; i++ {
		seedEvent(t, s, "w"+string(rune('0'+i)), "run-1", "function:y.go::Slow", "MODIFIED", now.Add(-time.Duration(i)*24*time.Hour))
	}
	// 2 edits -> below default minEdits of 3, must be excluded.
	for i := 0; i < 2; i++ {
		seedEvent(t, s, "u"+string(rune('0'+i)), "run-1", "function:z.go::Calm", "MODIFIED", now.Add(-time.Duration(i)*time.Minute))
	}

	rows, err := s.Thrashing(0, 0) // defaults: minEdits 3, lookback 7 days
	if err != nil {
		t.Fatalf("thrashing: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want exactly 1 (Hot only): %+v", len(rows), rows)
	}
	r := rows[0]
	if r.Signature != "function:x.go::Hot" {
		t.Errorf("signature = %s, want Hot", r.Signature)
	}
	if r.EditCount != 4 {
		t.Errorf("edit_count = %d, want 4", r.EditCount)
	}
	if r.WindowHours > 0.75+1e-9 {
		t.Errorf("window_hours = %v, want <= 0.75 (45min span)", r.WindowHours)
	}

	// Lookback narrowing: with a 1-day lookback the 3-day-old Slow edits fall outside.
	narrow, err := s.Thrashing(3, 1)
	if err != nil {
		t.Fatalf("thrashing narrow: %v", err)
	}
	for _, row := range narrow {
		if row.Signature == "function:y.go::Slow" {
			t.Errorf("Slow appeared inside 1-day lookback: %+v", row)
		}
	}
}

func TestThrashing_OutsideLookback(t *testing.T) {
	s := openTestStore(t)
	old := time.Now().UTC().Add(-30 * 24 * time.Hour)
	for i := 0; i < 5; i++ {
		seedEvent(t, s, "old"+string(rune('0'+i)), "", "function:ancient.go::Old", "MODIFIED", old.Add(time.Duration(i)*time.Minute))
	}
	rows, err := s.Thrashing(3, 7)
	if err != nil {
		t.Fatalf("thrashing: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("30-day-old edits must not appear in a 7-day lookback: %+v", rows)
	}
}

// seedEventIn is seedEvent with an explicit file path — FileHealth filters
// by file_path, so those tests must control it precisely.
func seedEventIn(t *testing.T, s *Store, id, runID, path, sig, action string, at time.Time) {
	t.Helper()
	err := s.InsertEvent(EventRecord{
		EventID:    id,
		RunID:      runID,
		RepoName:   "test",
		FilePath:   path,
		Signature:  sig,
		NodeType:   "function",
		Action:     action,
		BodyHash:   "h" + id,
		LOC:        3,
		OccurredAt: at,
	})
	if err != nil {
		t.Fatalf("seed event %s: %v", id, err)
	}
}

func TestFileHealth_Gradients(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC()

	// Cold file: full score.
	h, err := s.FileHealth("cold.go")
	if err != nil {
		t.Fatalf("file health cold: %v", err)
	}
	if h.HealthScore != 100 || h.IsFragile || h.RecentThrashingCount != 0 {
		t.Errorf("cold file = %+v, want score 100, not fragile", h)
	}

	// 2 edits (<5): penalty 16, not fragile, no warning.
	for i := 0; i < 2; i++ {
		seedEventIn(t, s, "h2"+string(rune('0'+i)), "", "f.go", "function:f.go::A", "MODIFIED", now.Add(-time.Duration(i)*time.Minute))
	}
	h2, err := s.FileHealth("f.go")
	if err != nil {
		t.Fatalf("file health f.go: %v", err)
	}
	if h2.HealthScore != 84 || h2.IsFragile || h2.Warning != "" {
		t.Errorf("2 edits = %+v, want score 84 not fragile", h2)
	}

	// 5 edits across 3 signatures: penalty 40 + 5 = 45, fragile, warning mentions counts.
	// (Exactly 5 events: S1 x2, S2 x2, S3 x1 — a 3x2 loop would seed 6 and
	// expect the wrong penalty.)
	fiveSeeds := []string{"S1", "S1", "S2", "S2", "S3"}
	for k, sig := range fiveSeeds {
		seedEventIn(t, s, "h5"+string(rune('a'+k)), "", "g.go", "function:g.go::"+sig, "MODIFIED", now.Add(-time.Duration(k)*time.Minute))
	}
	h5, err := s.FileHealth("g.go")
	if err != nil {
		t.Fatalf("file health g.go: %v", err)
	}
	if h5.HealthScore != 55 {
		t.Errorf("5 edits/3 sigs score = %d, want 55", h5.HealthScore)
	}
	if !h5.IsFragile || !strings.Contains(h5.Warning, "5 edits") {
		t.Errorf("5 edits must be fragile with warning, got %+v", h5)
	}

	// Saturation: many edits clamp the score at 0 without going negative.
	for i := 0; i < 10; i++ {
		seedEventIn(t, s, "hs"+string(rune('a'+i)), "", "g.go", "function:g.go::S1", "MODIFIED", now.Add(-time.Duration(i)*time.Minute))
	}
	hs, err := s.FileHealth("g.go")
	if err != nil {
		t.Fatalf("file health saturated: %v", err)
	}
	if hs.HealthScore != 0 {
		t.Errorf("saturated score = %d, want 0 (clamped)", hs.HealthScore)
	}
}

func TestFileHealth_IgnoresOtherFilesAndStaleEvents(t *testing.T) {
	s := openTestStore(t)
	now := time.Now().UTC()
	// Recent events on a DIFFERENT file...
	for i := 0; i < 5; i++ {
		seedEventIn(t, s, "o"+string(rune('0'+i)), "", "other.go", "function:other.go::O", "MODIFIED", now.Add(-time.Duration(i)*time.Minute))
	}
	// ...and stale events (2 days old) on the target file.
	for i := 0; i < 5; i++ {
		seedEventIn(t, s, "s"+string(rune('0'+i)), "", "target.go", "function:target.go::T", "MODIFIED", now.Add(-48*time.Hour-time.Duration(i)*time.Minute))
	}
	h, err := s.FileHealth("target.go")
	if err != nil {
		t.Fatalf("file health: %v", err)
	}
	if h.RecentThrashingCount != 0 || h.HealthScore != 100 || h.IsFragile {
		t.Errorf("stale+foreign events must not affect health: %+v", h)
	}
	// Sanity: the other file's own health IS degraded — proves the events landed.
	ho, err := s.FileHealth("other.go")
	if err != nil {
		t.Fatalf("file health other.go: %v", err)
	}
	if ho.RecentThrashingCount != 5 || ho.HealthScore == 100 {
		t.Errorf("other.go should show 5 recent edits and a penalty: %+v", ho)
	}
}
