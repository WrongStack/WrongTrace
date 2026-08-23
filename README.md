# WrongTrace

<div align="center">

**Universal AI Observability, AST Churn Intelligence, and Model Telemetry Hub for Autonomous Coding Agents.**

Watches your code with Tree-sitter, correlates AST-level edits with the agent runs that produced them, ingests OpenTelemetry/profiler runtime traces, provides a transparent AI Gateway, auto-discovers 20+ coding agents, and serves an interactive React dashboard with rich telemetry charts — all from one Go binary with the UI embedded via `//go:embed`.

[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go)](https://golang.org)
[![License: BUSL-1.1](https://img.shields.io/badge/License-BUSL--1.1-blue.svg)](LICENSE)
[![React](https://img.shields.io/badge/React-19-61DAFB?style=flat&logo=react)](https://react.dev)
[![TypeScript](https://img.shields.io/badge/TypeScript-5.5+-3178C6?style=flat&logo=typescript)](https://www.typescriptlang.org)
[![Vite](https://img.shields.io/badge/Vite-8-646CFF?style=flat&logo=vite)](https://vitejs.dev)
[![Tailwind CSS](https://img.shields.io/badge/Tailwind-4-38B2AC?style=flat&logo=tailwind-css)](https://tailwindcss.com)
[![Recharts](https://img.shields.io/badge/Recharts-3-22c55e?style=flat)](https://recharts.org)

</div>

---

## 💡 Key Capabilities

1. **Semantic AST Churn & Code Survival** — Tracks the granular lifecycle of functions, methods, classes, and structs via Tree-sitter AST diffing across 11+ languages rather than naive line counters.
2. **Detect Agent Thrashing & Regressions** — Flags code nodes repeatedly rewritten, broken, or deleted $\ge 3\times$ in a 24-hour sliding window.
3. **True Token ROI ($ / survived node)** — Measures model cost efficiency by calculating dollars spent per surviving AST node after a 14-day maturity window versus discarded churn.
4. **Auto-Discovery for 20+ Coding Agents** — Zero-config transcript ingestion for WrongStack, Antigravity, Claude Code, Cursor, Windsurf, Cline/Roo, MiniMax Code, Kimi Code (Moonshot), ZCode, Devin, Trae, Goose, OpenHands, GitHub Copilot, Aider, Continue, and more.
5. **Model Context Protocol (MCP) Server** — Native stdio MCP server exposing `check_guardrail`, `get_file_health_score`, `report_telemetry`, `lock_file`, `unlock_file`, `report_file_read`, and `get_file_read_stats`.
6. **Transparent AI Gateway & Wire Telemetry** — Intercepts and analyzes raw LLM traffic (OpenAI, Anthropic, Gemini, DeepSeek, Groq, MiniMax, Moonshot), tracking prompt/completion/reasoning tokens, budget quotas, and prompt cache savings.
7. **Universal Profiler & Runtime Traces** — Correlates runtime performance and latency hotspots (via OpenTelemetry OTLP, pprof, test runners) directly with AI code modifications.

---

## ⚡ Quickstart

### 1. One-Command Setup (`wrongtrace init`)

Run in any workspace to generate agent rules (`AGENTS.md`, `CLAUDE.md`, `.cursorrules`), `.mcp.json`, and Git post-commit telemetry hooks:

```bash
wrongtrace init
```

### 2. Start the Daemon

```bash
# Start observer daemon on port 4318 watching current repo:
wrongtrace start

# Or with custom port and target workspace:
wrongtrace start --watch /path/to/project --port 4318
```

Open **<http://localhost:4318>** in your browser.

### 3. Diagnostics & Health Check

```bash
wrongtrace doctor
```

---

## 🌟 Flagship Ecosystem Integration: WrongStack

> [!TIP]
> ### 👑 [WrongStack](https://github.com/wrongstack/wrongstack) — The Native Autonomous Multi-Agent Developer Stack
> WrongTrace is natively built as the deep observability engine for **[WrongStack](https://github.com/wrongstack/wrongstack)** — the high-performance autonomous multi-agent developer platform.
> * **Zero-Overhead Native Lineage**: Auto-detects WrongStack workspaces, projects (`~/.wrongstack/projects.json`), and hierarchical multi-agent date/session trees.
> * **Direct IPC & Memory Bus**: Direct high-speed named pipe / domain socket telemetry streams between WrongStack orchestrators and WrongTrace Tree-sitter AST inspectors.
> * **Holistic Fleet Governance**: Monitor multi-agent swarm deployments, subagent delegations, and model efficiency at scale across entire software teams.

---

## 🤖 Supported Coding Agents

WrongTrace automatically detects, monitors, and analyzes telemetry from all major autonomous coding environments (WrongStack native + multi-vendor agents):

| Coding Agent | Developer / Provider | Ingestion Source | Key Capabilities |
|:---|:---|:---|:---|
| **⚡ WrongStack** | **[WrongStack](https://github.com/wrongstack/wrongstack)** | `~/.wrongstack/projects/`, `.wrongstack/` | **Native Flagship**: Zero-config project discovery, multi-agent session lineage, IPC bus |
| **Google Antigravity** | Google DeepMind | `~/.gemini/antigravity-cli/brain` | Dynamic model extraction (`gemini-3.7-flash`, `gemini-2.5-pro`), subagent tracking |
| **Claude Code** | Anthropic | `~/.claude/logs`, `.mcp.json` | Tool calls, thinking blocks, prompt cache savings |
| **Cursor** | Anysphere | `~/.cursor`, `AppData/Roaming/Cursor` | File edits, multi-file diffs, agent mode telemetry |
| **Windsurf** | Codeium | `~/.codeium/windsurf/logs` | Cascade flow logs, AST diff correlation |
| **Cline & Roo Code** | Open Source / VS Code | `globalStorage/saoudrizwan.claude-dev/tasks` | Task JSON parsing, tool usage, step-by-step cost |
| **MiniMax Code** | MiniMax / abab | `~/.minimax/sessions`, `~/.minimax/logs` | `minimax-text-01`, `abab6.5s` token pricing & churn |
| **Kimi Code** | Moonshot AI | `~/.kimi/sessions`, `~/.moonshot/logs` | `kimi-k2`, `moonshot-v1-128k` context tracking |
| **Replit Agent** | Replit | `~/.replit/agent`, `.replit` | Autonomous web & backend fullstack agent telemetry |
| **Zed AI** | Zed Industries | `~/.config/zed/conversations`, `.zed` | High-performance assistant & slash command traces |
| **ZCode** | Z.ai / Zhipu AI | `~/.zcode/tasks`, `~/.zcode/logs` | `zcode-1`, `glm-4-plus` task correlation |
| **Devin** | Cognition | `~/.devin/sessions` | Agent run telemetry and AST verification |
| **Trae** | ByteDance | `~/.trae/logs`, `~/.trae/sessions` | Multi-model IDE session tracking |
| **Goose** | Block / Square | `~/.goose/sessions` | Goose autonomous agent telemetry |
| **OpenHands** | All-Hands (OpenDevin) | `~/.openhands/conversations` | Conversation traces and code generation |
| **GitHub Copilot** | GitHub / Microsoft | `~/.copilot/logs`, `~/.config/github-copilot` | Workspace & CLI chat telemetry |
| **Aider** | Aider AI | `.aider.chat.history.md` | Markdown diff & git commit traces |
| **Continue.dev** | Continue | `~/.continue/sessions` | Session history & prompt telemetry |

---

## 🖥️ Dashboard Views

| View | Description |
|:---|:---|
| **📊 Overview** | Churn timeline, Net LOC delta, Spend vs. Waste KPI, Thrashing Alerts, Live Event Stream. |
| **🏆 Model Leaderboard** | Model survival rate %, Cost per survived AST node, True Token ROI, and rank badges. |
| **🗺️ Code Atlas** | Interactive AST graph with symbol-level health scores, churn heatmaps, and monorepo workspace scopes. |
| **🔍 Diffs & Changes** | Unified diff viewer with syntax highlighting and added/deleted line statistics. |
| **🤖 Agent Sessions & Catalog** | Multi-agent session monitor, live 2025/2026 model pricing registry, and interactive cost simulator. |
| **⚡ Profiler & Runtime Traces** | OpenTelemetry (OTLP) ingest, P50/P90/P99 latency percentiles, and latency hotspot analysis. |
| **🌐 AI Gateway & Wire Traffic** | Transparent proxy for LLM APIs, token composition analysis, budget quotas, and cache savings breakdown. |
| **⚙️ Settings & Governance** | Multi-project workspace management, SQLite vacuuming, retention pruning, guardrail file locking, and Webhooks. |

---

## 💻 CLI Command Reference

```text
Usage:
  wrongtrace [command]

Available Commands:
  start          Run observer daemon, file watcher, OTLP endpoint, and embedded dashboard
  init           Initialize WrongTrace configs, agent rules (CLAUDE.md, AGENTS.md), and git hooks
  doctor         Run comprehensive diagnostics on SQLite, IPC, agent logs, and Tree-sitter parsers
  mcp            Serve Model Context Protocol (MCP) over stdio for Claude, Cursor, Windsurf, Cline
  trace          Execute any command/test, measure runtime latency, and record profiler telemetry
  hook           Install or remove git hooks for automated telemetry and churn tracking
  export         Export telemetry and code churn records to JSON
  report         Generate an executive Markdown, HTML, or JSON observability report
  status         Print a quick summary of DB path, monitored repo, totals, and active models
```

---

## 🛡️ Model Context Protocol (MCP) Setup

Add WrongTrace to your favorite MCP client (`~/.claude.json`, `.cursor/mcp.json`, `windsurf_mcp.json`, etc.):

```json
{
  "mcpServers": {
    "wrongtrace": {
      "command": "wrongtrace",
      "args": ["mcp"]
    }
  }
}
```

### Available MCP Tools:
- **`check_guardrail`**: Check if a file is safe to modify before performing automated AI refactoring (enforces file locks).
- **`get_file_health_score`**: Get 0-100 health score, fragility status, and recent thrash events.
- **`report_telemetry`**: Record run intent, model name, token usage, and cost.
- **`lock_file`**: Lock fragile files against concurrent AI rewrites.
- **`unlock_file`**: Release locked files upon completion.
- **`report_file_read`**: Record file reading activity with line range and token consumption.
- **`get_file_read_stats`**: Query aggregate read counts, unique models, and cost per file.

---

## 🌐 AI Gateway Reverse Proxy

Route any SDK (OpenAI SDK, Anthropic SDK, LangChain, LiteLLM, Ollama) through WrongTrace to automatically capture wire tokens, reasoning tokens (`<think>`), and prompt caching savings without code changes:

```bash
# OpenAI SDK / Compatible
export OPENAI_BASE_URL="http://localhost:4318/proxy/api.openai.com/v1"

# Anthropic SDK
export ANTHROPIC_BASE_URL="http://localhost:4318/proxy/api.anthropic.com"

# Google Gemini
export GEMINI_API_BASE="http://localhost:4318/proxy/generativelanguage.googleapis.com"
```

---

## 📄 License & Commercial Terms

WrongTrace is licensed under the **[Business Source License 1.1 (BUSL-1.1)](LICENSE)**:

* **Free for Everyone**: Free to use for personal projects, internal development, private testing, and internal team observability within your own organization without restrictions.
* **Commercial Hosted Protection**: Offering WrongTrace as a paid managed service, hosted SaaS, or commercial observability product to third parties requires a commercial license from [WrongStack](https://github.com/wrongstack).
* **Automatic Open Source Conversion**: Converts automatically to standard **Apache License, Version 2.0** on `2030-01-01`.

For enterprise licensing and hosted deployment inquiries, visit **[github.com/wrongstack](https://github.com/wrongstack)**.


