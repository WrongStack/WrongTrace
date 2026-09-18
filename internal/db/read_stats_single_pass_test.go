package db

import (
	"fmt"
	"testing"
	"time"
)

// GetFileReadStats folds its aggregates, breakdowns and recent timeline out of
// one newest-first scan. Past the 20-row timeline cap the totals must still
// cover every row, and only the kept rows carry their intent.
func TestGetFileReadStats_SinglePassBeyondRecentCap(t *testing.T) {
	s := openTestStore(t)
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	const n = 30
	for i := 0; i < n; i++ {
		model, provider := "model-a", "prov-a"
		if i%3 == 0 {
			model, provider = "model-b", ""
		}
		if err := s.InsertReadEvent(FileReadRecord{
			ReadID:         fmt.Sprintf("r-%02d", i),
			RepoName:       "repo",
			FilePath:       "pkg/x.go",
			AgentName:      "agent",
			ModelName:      model,
			Provider:       provider,
			ToolName:       "read_file",
			LinesReadCount: 10,
			PromptTokens:   100,
			CachedTokens:   5,
			CostUSD:        0.5,
			Intent:         fmt.Sprintf("intent-%02d", i),
			ReadTime:       base.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	// A different file must not leak into the stats.
	_ = s.InsertReadEvent(FileReadRecord{ReadID: "other", RepoName: "repo", FilePath: "pkg/y.go",
		AgentName: "agent", ModelName: "model-z", Provider: "p", ToolName: "read_file", ReadTime: base})

	stats, err := s.GetFileReadStats("pkg/x.go")
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalReads != n || stats.TotalLinesRead != 10*n || stats.TotalPromptTokens != 100*n ||
		stats.TotalCachedTokens != 5*n || stats.TotalCostUSD < 0.5*n-1e-9 || stats.TotalCostUSD > 0.5*n+1e-9 {
		t.Errorf("totals = %+v", stats)
	}
	if stats.UniqueModels != 2 || stats.ModelBreakdown["model-b"] != 10 || stats.ModelBreakdown["model-a"] != 20 {
		t.Errorf("models: unique=%d breakdown=%v", stats.UniqueModels, stats.ModelBreakdown)
	}
	if len(stats.ProviderBreakdown) != 1 || stats.ProviderBreakdown["prov-a"] != 20 {
		t.Errorf("providers = %v (empty provider must be skipped)", stats.ProviderBreakdown)
	}
	if len(stats.RecentReads) != 20 {
		t.Fatalf("recent = %d, want 20", len(stats.RecentReads))
	}
	for i, r := range stats.RecentReads {
		want := fmt.Sprintf("r-%02d", n-1-i)
		if r.ReadID != want || r.Intent != "intent-"+want[2:] {
			t.Errorf("recent[%d] = %s/%q, want %s newest-first with intent", i, r.ReadID, r.Intent, want)
		}
	}
}
