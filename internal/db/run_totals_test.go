package db

import (
	"path/filepath"
	"testing"
)

func TestRunTotals_ReadsStoredRun(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok, err := s.RunTotals("missing"); ok || err != nil {
		t.Fatalf("unknown run: ok=%v err=%v; want false, nil", ok, err)
	}
	if err := s.UpsertRun(RunRecord{RunID: "r1", ModelName: "m", PromptTokens: 120, CompletionTokens: 60, CostUSD: 0.5}); err != nil {
		t.Fatal(err)
	}
	p, c, cost, ok, err := s.RunTotals("r1")
	if err != nil || !ok || p != 120 || c != 60 || cost != 0.5 {
		t.Fatalf("RunTotals(r1) = %d/%d $%v ok=%v err=%v; want 120/60 $0.5", p, c, cost, ok, err)
	}
}
