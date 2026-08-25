# WrongTrace

<div align="center">

**Universal AI Observability, AST Churn Intelligence, and Model Telemetry Hub for Autonomous Coding Agents.**

Watches your code with Tree-sitter, correlates AST-level edits with path-scoped agent tool operations, ingests OpenTelemetry/profiler runtime traces, tracks inter-agent code collisions ("Who Broke Whose Code?"), provides an interactive Code Atlas with full-screen graph visualization, serves an embedded React dashboard, and operates an AI Gateway observer — all from a single, high-performance Go binary.

[![Version](https://img.shields.io/badge/Version-0.3.7-blue.svg?style=flat)](CHANGELOG.md)
[![Go Version](https://img.shields.io/badge/Go-1.25+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![License: BUSL-1.1](https://img.shields.io/badge/License-BUSL--1.1-purple.svg)](LICENSE)
[![React](https://img.shields.io/badge/React-19-61DAFB?style=flat&logo=react)](https://react.dev)
[![TypeScript](https://img.shields.io/badge/TypeScript-7.0-3178C6?style=flat&logo=typescript)](https://www.typescriptlang.org)
[![Vite](https://img.shields.io/badge/Vite-8-646CFF?style=flat&logo=vite)](https://vitejs.dev)
[![Tailwind CSS](https://img.shields.io/badge/Tailwind-4-38B2AC?style=flat&logo=tailwind-css)](https://tailwindcss.com)

</div>

---

## 💡 Key Capabilities

1. **🕸️ Inter-Agent Friction & Cross-Thrashing Matrix ("Who Broke Whose Code?")** — Uncovers multi-agent code collisions and friction. Tracks which AI model originally authored an AST node and which subsequent model rewrote or deleted it, complete with time-delta durations ($\Delta T$) and inline syntax-highlighted diffs.
2. **🔍 Live Named Pipe & IPC Inspector (`\\.\pipe\wrongtrace`)** — Real-time telemetry and JSON-RPC wire inspector for WrongStack and local autonomous agents, exposing request payloads, daemon replies, and execution latency.
3. **🗺️ Interactive Next-Gen Code Atlas** — Full-screen interactive architectural map with Orbital Radial, Hierarchical Tree, and Grid layout algorithms. Features intelligent smooth auto-centering (`FlowAutoArranger`), Health Score heatmaps, Churn Velocity modes, and multi-tab symbol lineage drawers.
4. **📜 AST Symbol Evolution & Lineage History** — Chronological lifecycle tracking for every function, method, class, and struct. Inspect exact revisions, model attributions, and localized diffs across the entire history of the symbol.
5. **🧠 Model Intelligence & True Token ROI Matrix** — Grades AI models into Quality Tiers (S/A/B/C) based on 14-day code survival rate %, dollar expenditure per surviving node ($/node), context read-to-write ratio, and longevity.
6. **🔍 Semantic AST Churn & Accurate Multi-Line Diffing** — Tracks code transformations via Tree-sitter AST diffing across 11+ languages with exact `+AddedLines / -DeletedLines` granularity, filtering out cosmetic formatting churn.
7. **🤖 Universal Auto-Discovery for 20+ Coding Agents** — Zero-config transcript ingestion for WrongStack, Antigravity, Claude Code, Cursor, Windsurf, Cline/Roo, MiniMax Code, Kimi Code (Moonshot), ZCode, Devin, Trae, Goose, OpenHands, GitHub Copilot, Aider, Continue, and more.
8. **🛡️ Model Context Protocol (MCP) Server** — Native stdio MCP server exposing `check_guardrail`, `get_file_health_score`, `report_telemetry`, `lock_file`, `unlock_file`, `report_file_read`, and `get_file_read_stats`.
9. **🌐 AI Gateway & Wire Telemetry** — Relays and analyzes LLM traffic (OpenAI, Anthropic, Gemini, DeepSeek, Groq, MiniMax, Moonshot), tracking prompt/completion/reasoning tokens, budget quotas, and optional scoped response-cache savings.
10. **⚡ Universal Profiler & Runtime Traces** — Ingests OpenTelemetry (OTLP) traces, pprof profiles, and test execution latencies, correlating runtime hotspots directly with AI code changes.

---

## ⚡ Quickstart

### 1. One-Command Setup (`wrongtrace init`)

Run in any workspace to generate agent rules (`AGENTS.md`, `CLAUDE.md`, `.cursorrules`), `.mcp.json`, and Git post-commit telemetry hooks:

```bash
wrongtrace init
```

### 2. Start the Observer Daemon

```bash
# Start daemon on unified port 3444 watching current workspace:
wrongtrace start

# Or with custom port and target directory:
wrongtrace start --watch /path/to/project --port 3444
```

Open **<http://localhost:3444>** in your browser.

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
> * **Direct IPC & Memory Bus**: High-speed named pipe / domain socket telemetry streams between WrongStack orchestrators and WrongTrace Tree-sitter AST inspectors.
> * **Holistic Fleet Governance**: Monitor multi-agent swarm deployments, subagent delegations, and model efficiency at scale across entire software teams.

---

## 🤖 Supported Coding Agents

WrongTrace automatically detects, monitors, and analyzes telemetry from all major autonomous coding environments:

| Coding Agent | Developer / Provider | Ingestion Source | Key Capabilities |
|:---|:---|:---|:---|
| **⚡ WrongStack** | **[WrongStack](https://github.com/wrongstack/wrongstack)** | `~/.wrongstack/projects/`, `.wrongstack/` | **Native Flagship**: Zero-config discovery, multi-agent session lineage, high-speed IPC bus |
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
| **📊 Overview** | Churn timeline, Net LOC delta, Spend vs. Waste KPI, Thrashing alerts, Live event stream. |
| **⚔️ Inter-Agent Friction** | Collision Heatmap Grid, "Who Broke Whose Code?" timeline, $\Delta T$ duration, and overwrite diffs. |
| **🧠 Model Intelligence** | S/A/B/C Quality Tiers, 14-day code survival rate %, True Token ROI ($/node), and Context Read matrix. |
| **🗺️ Code Atlas** | Full-screen interactive AST graph with smooth auto-centering, Orbit/Tree/Grid layouts, and lineage drawer. |
| **🔍 Diffs & Changes** | Full-page rich diff inspector with syntax highlighting and added/deleted line statistics. |
| **📜 Symbol Lineage** | Chronological revision timeline for any AST node with author model attribution and historical diffs. |
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
export OPENAI_BASE_URL="http://localhost:3444/proxy/api.openai.com/v1"

# Anthropic SDK
export ANTHROPIC_BASE_URL="http://localhost:3444/proxy/api.anthropic.com"

# Google Gemini
export GEMINI_API_BASE="http://localhost:3444/proxy/generativelanguage.googleapis.com"
```

Response caching is disabled unless a request explicitly sends
`X-WrongTrace-Cache: allow`. Cache keys are isolated by credential, project,
agent, and session scope; authorization material itself is never stored.

The gateway is byte-preserving by default. Secret redaction, quota blocking,
OpenAI usage-option injection, and missing terminal-marker repair are mutating
guardrails and require `X-WrongTrace-Policy: enforce`.

---

## 📄 License & Commercial Terms

WrongTrace is licensed under the **[Business Source License 1.1 (BUSL-1.1)](LICENSE)**:

* **Free for Everyone**: Free to use for personal projects, internal development, private testing, and internal team observability within your own organization without restrictions.
* **Commercial Hosted Protection**: Offering WrongTrace as a paid managed service, hosted SaaS, or commercial observability product to third parties requires a commercial license from [WrongStack](https://github.com/wrongstack).
* **Automatic Open Source Conversion**: Converts automatically to standard **Apache License, Version 2.0** on `2030-01-01`.

For enterprise licensing and hosted deployment inquiries, visit **[github.com/wrongstack](https://github.com/wrongstack)**.
