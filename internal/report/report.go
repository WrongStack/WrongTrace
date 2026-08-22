package report

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wrongstack/wrongtrace/internal/core"
)

// GenerateMarkdownReport produces an exportable GitHub PR / Executive Markdown summary.
func GenerateMarkdownReport(snap core.MetricsSnapshot) string {
	var b strings.Builder

	b.WriteString("# 🎯 WrongTrace — AI Observability & Token ROI Report\n\n")
	b.WriteString(fmt.Sprintf("> **Repository:** `%s` · **Generated:** `%s`\n\n", snap.Repo, snap.GeneratedAt.Format(time.RFC822)))

	// 1. Executive Summary
	b.WriteString("## 📊 Executive Summary\n\n")
	b.WriteString("| Metric | Value |\n")
	b.WriteString("| :--- | :--- |\n")
	b.WriteString(fmt.Sprintf("| **Total Agent Runs** | `%d` |\n", snap.Overview.TotalRuns))
	b.WriteString(fmt.Sprintf("| **Total AST Events Recorded** | `%d` |\n", snap.Overview.TotalEvents))
	b.WriteString(fmt.Sprintf("| **Total AI Spend** | `$%.2f` |\n", snap.Overview.TotalCost))
	b.WriteString(fmt.Sprintf("| **Active Thrashing Symbols** | `%d` |\n", len(snap.Thrashing)))
	b.WriteString(fmt.Sprintf("| **Active Agent Runs** | `%d` |\n", len(snap.ActiveRuns)))
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
			b.WriteString(fmt.Sprintf("| **`%s`** | `%.1f%%` | `%d` | `$%.2f` | `%s` |\n",
				m.Model, m.SurvivalRatePct, m.TotalSurvivedNodes, m.TotalCostUSD, costStr))
		}
		b.WriteString("\n")
	}

	// 3. Thrashing Nodes
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

	// 4. Recent AST Events
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

// GenerateJSONReport serializes the metrics snapshot into pretty-printed JSON.
func GenerateJSONReport(snap core.MetricsSnapshot) (string, error) {
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}
