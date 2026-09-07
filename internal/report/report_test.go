package report

import (
	"strings"
	"testing"
	"time"

	"github.com/wrongstack/wrongtrace/internal/core"
	"github.com/wrongstack/wrongtrace/internal/db"
)

func TestGenerateReports(t *testing.T) {
	data := ReportData{
		Snapshot: core.MetricsSnapshot{
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
		},
		ProfilerOverview: db.ProfilerOverviewRow{
			TotalTraces:    50,
			TotalErrors:    2,
			AvgDurationMs:  34.5,
			ActiveServices: 3,
		},
		Hotspots: []db.ProfilerHotspotRow{
			{
				NodeSignature: "func:calc.go::Add",
				FilePath:      "calc.go",
				TraceCount:    25,
				AvgDurationMs: 40.2,
				TotalErrors:   1,
			},
		},
	}

	md := GenerateMarkdownReport(data)
	if !strings.Contains(md, "WrongTrace") || !strings.Contains(md, "test-repo") {
		t.Error("missing header or repo in markdown report")
	}
	if !strings.Contains(md, "claude-3-7-sonnet") || !strings.Contains(md, "85.5%") {
		t.Error("missing model leaderboard in markdown report")
	}
	if !strings.Contains(md, "main.go") || !strings.Contains(md, "calc.go") {
		t.Error("missing thrashing or recent event rows in markdown report")
	}

	// Test HTML Report
	htmlStr := GenerateHTMLReport(data)
	if !strings.Contains(htmlStr, "WrongTrace Observability Report") || !strings.Contains(htmlStr, "test-repo") {
		t.Errorf("GenerateHTMLReport failed: %s", htmlStr)
	}

	// Test JSON Report
	jsonStr, err := GenerateJSONReport(data)
	if err != nil || !strings.Contains(jsonStr, `"test-repo"`) {
		t.Errorf("GenerateJSONReport failed: %v", err)
	}
}

// TestGenerateMarkdownReport_EscapesTableCells pins the round-33 fix: every
// telemetry-derived string in a Markdown table cell is rendered inert, so a
// hostile payload cannot split cells/rows or inject live Markdown into the
// exported PR/executive summary.
func TestGenerateMarkdownReport_EscapesTableCells(t *testing.T) {
	data := ReportData{
		Snapshot: core.MetricsSnapshot{
			Repo:        "wrong|repo",
			GeneratedAt: time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC),
			Overview:    db.Overview{TotalRuns: 3, TotalEvents: 9, TotalCost: 0.5},
			Models: []db.ModelRow{
				{
					Model:              "mdl`x",
					SurvivalRatePct:    50,
					TotalSurvivedNodes: 1,
					TotalCostUSD:       0.1,
					CostPerSurvNode:    0.1,
				},
			},
			Thrashing: []db.ThrashingRow{
				{
					FilePath:    "thrash|file.go",
					Signature:   "sig\nINJECTED_ROW",
					EditCount:   4,
					WindowHours: 2,
				},
			},
			RecentEvents: []db.EventRecord{
				{
					FilePath:   "evt|file.go",
					Signature:  "back\\|slash.go",
					RunID:      "run|77",
					Action:     "MODIFIED",
					OccurredAt: time.Date(2026, 9, 6, 12, 1, 0, 0, time.UTC),
				},
			},
		},
		ProfilerOverview: db.ProfilerOverviewRow{TotalTraces: 2, AvgDurationMs: 5},
		Hotspots: []db.ProfilerHotspotRow{
			{
				NodeSignature: "hot`node",
				FilePath:      "hot|path.go",
				TraceCount:    3,
				AvgDurationMs: 1.5,
			},
		},
	}

	md := GenerateMarkdownReport(data)

	for _, leak := range []string{
		"wrong|repo", "mdl`x", "hot`node", "hot|path.go",
		"thrash|file.go", "evt|file.go", "run|77",
		"sig\nINJECTED_ROW", // raw newline must never split a row
		"back\\|slash.go",   // payload backslash must not defeat the pipe escape
	} {
		if strings.Contains(md, leak) {
			t.Errorf("markdown report leaks raw payload %q", leak)
		}
	}
	for _, inert := range []string{
		`wrong\|repo`, "mdl'x", "hot'node", `hot\|path.go`,
		`thrash\|file.go`, `evt\|file.go`, `run\|77`,
		"sig INJECTED_ROW",
		`back\\\|slash.go`,
	} {
		if !strings.Contains(md, inert) {
			t.Errorf("markdown report missing inert form %q", inert)
		}
	}
	if !strings.Contains(md, "| `MODIFIED` |") {
		t.Error("benign action cell must render unchanged")
	}
}

// TestMDCell covers the mdCell boundary branches directly: clean payloads pass
// through untouched, and each structural character class is neutralized.
func TestMDCell(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},                   // empty passthrough
		{"func:main", "func:main"}, // clean passthrough
		{"a|b", `a\|b`},            // cell delimiter
		{"a`b", "a'b"},             // code-span delimiter
		{"a\nb", "a b"},            // row split
		{"a\r\nb", "a b"},          // CRLF collapses to a single space
		{"a\rb", "a b"},            // bare CR
		{`a\b`, `a\\b`},            // lone backslash
		{`a\|b`, `a\\\|b`},         // backslash escaped first keeps the pipe escape intact
	}
	for _, c := range cases {
		if got := mdCell(c.in); got != c.want {
			t.Errorf("mdCell(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
