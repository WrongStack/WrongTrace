package report

import (
	"strings"
	"testing"
	"time"

	"github.com/wrongstack/wrongtrace/internal/core"
	"github.com/wrongstack/wrongtrace/internal/db"
)

func TestGenerateMarkdownReport(t *testing.T) {
	snap := core.MetricsSnapshot{
		Repo:        "test-repo",
		GeneratedAt: time.Now().UTC(),
		Overview: db.Overview{
			TotalRuns:   10,
			TotalEvents: 45,
			TotalCost:   3.50,
		},
		Models: []db.ModelRow{
			{
				Model:              "claude-3-7-sonnet",
				SurvivalRatePct:    85.5,
				TotalSurvivedNodes: 12,
				TotalCostUSD:       2.50,
				CostPerSurvNode:    0.2083,
			},
		},
		Thrashing: []db.ThrashingRow{
			{
				FilePath:    "main.go",
				Signature:   "func:main",
				EditCount:   4,
				WindowHours: 2.5,
			},
		},
		RecentEvents: []db.EventRecord{
			{
				FilePath:   "calc.go",
				Signature:  "func:Add",
				RunID:      "run-101",
				Action:     "MODIFIED",
				OccurredAt: time.Now().UTC(),
			},
		},
	}

	md := GenerateMarkdownReport(snap)
	if !strings.Contains(md, "WrongTrace") || !strings.Contains(md, "test-repo") {
		t.Error("missing header or repo in markdown report")
	}
	if !strings.Contains(md, "claude-3-7-sonnet") || !strings.Contains(md, "85.5%") {
		t.Error("missing model leaderboard in markdown report")
	}
	if !strings.Contains(md, "main.go") || !strings.Contains(md, "calc.go") {
		t.Error("missing thrashing or recent event rows in markdown report")
	}

	// Test JSON Report
	jsonStr, err := GenerateJSONReport(snap)
	if err != nil || !strings.Contains(jsonStr, `"test-repo"`) {
		t.Errorf("GenerateJSONReport failed: %v", err)
	}
}
