package report

import (
	"encoding/json"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/wrongstack/wrongtrace/internal/core"
	"github.com/wrongstack/wrongtrace/internal/db"
)

// ReportData holds the complete metrics and profiler dataset for report generation.
type ReportData struct {
	Snapshot         core.MetricsSnapshot
	ProfilerOverview db.ProfilerOverviewRow
	Hotspots         []db.ProfilerHotspotRow
}

// GenerateMarkdownReport produces an exportable GitHub PR / Executive Markdown summary.
func GenerateMarkdownReport(data ReportData) string {
	snap := data.Snapshot
	var b strings.Builder

	b.WriteString("# 🎯 WrongTrace — AI Observability & Telemetry Report\n\n")
	b.WriteString(fmt.Sprintf("> **Repository:** `%s` · **Generated:** `%s`\n\n", snap.Repo, snap.GeneratedAt.Format(time.RFC822)))

	// 1. Executive Summary
	b.WriteString("## 📊 Executive Summary\n\n")
	b.WriteString("| Metric | Value |\n")
	b.WriteString("| :--- | :--- |\n")
	b.WriteString(fmt.Sprintf("| **Total Agent Runs** | `%d` |\n", snap.Overview.TotalRuns))
	b.WriteString(fmt.Sprintf("| **Total AST Events Recorded** | `%d` |\n", snap.Overview.TotalEvents))
	b.WriteString(fmt.Sprintf("| **Total AI Spend** | `$%.4f` |\n", snap.Overview.TotalCost))
	b.WriteString(fmt.Sprintf("| **Runtime Execution Traces** | `%d` |\n", data.ProfilerOverview.TotalTraces))
	b.WriteString(fmt.Sprintf("| **Mean Execution Latency** | `%.2f ms` |\n", data.ProfilerOverview.AvgDurationMs))
	b.WriteString(fmt.Sprintf("| **Active Thrashing Symbols** | `%d` |\n", len(snap.Thrashing)))
	b.WriteString("\n")

	// 2. Model Leaderboard
	b.WriteString("## 🏆 AI Model Survival & Cost Leaderboard\n\n")
	if len(snap.Models) == 0 {
		b.WriteString("_No model telemetry recorded yet._\n\n")
	} else {
		b.WriteString("| Model | Survival Rate | Survived Nodes | Total Cost | Cost / Survived Node |\n")
		b.WriteString("| :--- | :---: | :---: | :---: | :---: |\n")
		for _, m := range snap.Models {
			costStr := fmt.Sprintf("$%.4f", m.CostPerSurvNode)
			if m.TotalSurvivedNodes == 0 {
				costStr = "—"
			}
			b.WriteString(fmt.Sprintf("| **`%s`** | `%.1f%%` | `%d` | `$%.4f` | `%s` |\n",
				m.Model, m.SurvivalRatePct, m.TotalSurvivedNodes, m.TotalCostUSD, costStr))
		}
		b.WriteString("\n")
	}

	// 3. Runtime Latency Hotspots
	if len(data.Hotspots) > 0 {
		b.WriteString("## ⏱ Runtime Latency Hotspots\n\n")
		b.WriteString("| Function / Symbol | File | Avg Latency | Calls | Errors |\n")
		b.WriteString("| :--- | :--- | :---: | :---: | :---: |\n")
		for _, h := range data.Hotspots {
			b.WriteString(fmt.Sprintf("| `%s` | `%s` | `%.2f ms` | `%d` | `%d` |\n",
				h.NodeSignature, h.FilePath, h.AvgDurationMs, h.TraceCount, h.TotalErrors))
		}
		b.WriteString("\n")
	}

	// 4. Thrashing Nodes
	if len(snap.Thrashing) > 0 {
		b.WriteString("## ⚠️ High Churn / Thrashing Code Nodes (≥3 edits in 24h)\n\n")
		b.WriteString("| File | Symbol | Edits | Window |\n")
		b.WriteString("| :--- | :--- | :---: | :---: |\n")
		for _, t := range snap.Thrashing {
			b.WriteString(fmt.Sprintf("| `%s` | `%s` | `%d` | `%.1fh` |\n",
				t.FilePath, t.Signature, t.EditCount, t.WindowHours))
		}
		b.WriteString("\n")
	}

	// 5. Recent AST Events
	if len(snap.RecentEvents) > 0 {
		b.WriteString("## 📜 Recent AST Modifications\n\n")
		b.WriteString("| File | Symbol | Action | Run ID | When |\n")
		b.WriteString("| :--- | :--- | :---: | :--- | :---: |\n")
		for i, r := range snap.RecentEvents {
			if i >= 10 {
				break
			}
			runStr := r.RunID
			if runStr == "" {
				runStr = "—"
			}
			b.WriteString(fmt.Sprintf("| `%s` | `%s` | `%s` | `%s` | `%s` |\n",
				r.FilePath, r.Signature, r.Action, runStr, r.OccurredAt.Format("15:04:05")))
		}
		b.WriteString("\n")
	}

	b.WriteString("---\n")
	b.WriteString("_Generated automatically by [WrongTrace](https://github.com/wrongstack/wrongtrace)._\n")

	return b.String()
}

// GenerateHTMLReport renders a standalone dark-themed HTML report.
func GenerateHTMLReport(data ReportData) string {
	snap := data.Snapshot

	var modelsTable strings.Builder
	if len(snap.Models) > 0 {
		modelsTable.WriteString("<table><thead><tr><th>Model</th><th>Survival Rate</th><th>Survived Nodes</th><th>Total Cost</th><th>Cost / Survived Node</th></tr></thead><tbody>")
		for _, m := range snap.Models {
			costStr := fmt.Sprintf("$%.4f", m.CostPerSurvNode)
			if m.TotalSurvivedNodes == 0 {
				costStr = "—"
			}
			modelsTable.WriteString(fmt.Sprintf("<tr><td><code>%s</code></td><td>%.1f%%</td><td>%d</td><td>$%.4f</td><td>%s</td></tr>",
				html.EscapeString(m.Model), m.SurvivalRatePct, m.TotalSurvivedNodes, m.TotalCostUSD, costStr))
		}
		modelsTable.WriteString("</tbody></table>")
	} else {
		modelsTable.WriteString("<p style=\"color: #64748b;\">No model telemetry recorded yet.</p>")
	}

	var hotspotsTable strings.Builder
	if len(data.Hotspots) > 0 {
		hotspotsTable.WriteString("<table><thead><tr><th>Symbol</th><th>File</th><th>Avg Latency</th><th>Calls</th><th>Errors</th></tr></thead><tbody>")
		for _, h := range data.Hotspots {
			hotspotsTable.WriteString(fmt.Sprintf("<tr><td><code>%s</code></td><td><code>%s</code></td><td>%.2f ms</td><td>%d</td><td>%d</td></tr>",
				html.EscapeString(h.NodeSignature), html.EscapeString(h.FilePath), h.AvgDurationMs, h.TraceCount, h.TotalErrors))
		}
		hotspotsTable.WriteString("</tbody></table>")
	}

	var thrashingTable strings.Builder
	if len(snap.Thrashing) > 0 {
		thrashingTable.WriteString("<table><thead><tr><th>File</th><th>Symbol</th><th>Edits</th><th>Window</th></tr></thead><tbody>")
		for _, t := range snap.Thrashing {
			thrashingTable.WriteString(fmt.Sprintf("<tr><td><code>%s</code></td><td><code>%s</code></td><td><span class=\"badge\">%d edits</span></td><td>%.1fh</td></tr>",
				html.EscapeString(t.FilePath), html.EscapeString(t.Signature), t.EditCount, t.WindowHours))
		}
		thrashingTable.WriteString("</tbody></table>")
	}

	hotspotsSection := ""
	if hotspotsTable.Len() > 0 {
		hotspotsSection = fmt.Sprintf("<div class=\"card\"><h2>⏱ Runtime Latency Hotspots</h2>%s</div>", hotspotsTable.String())
	}

	thrashingSection := ""
	if thrashingTable.Len() > 0 {
		thrashingSection = fmt.Sprintf("<div class=\"card\"><h2>⚠️ High Churn / Thrashing Code Nodes</h2>%s</div>", thrashingTable.String())
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>WrongTrace Report - %s</title>
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; background: #0b0f19; color: #e2e8f0; margin: 0; padding: 32px; }
    .container { max-width: 1000px; margin: 0 auto; }
    .card { background: #131b2e; border: 1px solid #1e293b; border-radius: 12px; padding: 24px; margin-bottom: 24px; }
    h1 { color: #6366f1; margin-top: 0; }
    h2 { color: #f8fafc; font-size: 1.25rem; border-bottom: 1px solid #1e293b; padding-bottom: 8px; margin-top: 0; }
    table { width: 100%%; border-collapse: collapse; margin-top: 12px; }
    th, td { text-align: left; padding: 10px 12px; border-bottom: 1px solid #1e293b; font-size: 0.875rem; }
    th { color: #94a3b8; font-weight: 600; }
    code { font-family: monospace; background: #0f172a; padding: 2px 6px; border-radius: 4px; color: #a5b4fc; }
    .badge { display: inline-block; padding: 2px 8px; border-radius: 9999px; font-size: 0.75rem; font-weight: 600; background: #1e1b4b; color: #818cf8; }
    .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 16px; margin-bottom: 8px; }
    .kpi { background: #1e293b; border-radius: 8px; padding: 16px; text-align: center; }
    .kpi-val { font-size: 1.75rem; font-weight: 700; color: #fff; margin-top: 4px; }
    .kpi-lbl { font-size: 0.75rem; color: #94a3b8; text-transform: uppercase; letter-spacing: 0.05em; }
  </style>
</head>
<body>
  <div class="container">
    <div class="card">
      <h1>🎯 WrongTrace Observability Report</h1>
      <p style="color: #94a3b8;">Repository: <code>%s</code> &bull; Generated: %s</p>
      
      <div class="grid">
        <div class="kpi">
          <div class="kpi-lbl">Total Agent Runs</div>
          <div class="kpi-val">%d</div>
        </div>
        <div class="kpi">
          <div class="kpi-lbl">Total AI Spend</div>
          <div class="kpi-val">$%.4f</div>
        </div>
        <div class="kpi">
          <div class="kpi-lbl">AST Code Events</div>
          <div class="kpi-val">%d</div>
        </div>
        <div class="kpi">
          <div class="kpi-lbl">Runtime Traces</div>
          <div class="kpi-val">%d</div>
        </div>
      </div>
    </div>

    <div class="card">
      <h2>🏆 AI Model Survival & Cost Leaderboard</h2>
      %s
    </div>

    %s
    %s
  </div>
</body>
</html>`,
		html.EscapeString(snap.Repo),
		html.EscapeString(snap.Repo),
		snap.GeneratedAt.Format(time.RFC822),
		snap.Overview.TotalRuns,
		snap.Overview.TotalCost,
		snap.Overview.TotalEvents,
		data.ProfilerOverview.TotalTraces,
		modelsTable.String(),
		hotspotsSection,
		thrashingSection,
	)
}

// GenerateJSONReport serializes the complete report data into pretty-printed JSON.
func GenerateJSONReport(data ReportData) (string, error) {
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

