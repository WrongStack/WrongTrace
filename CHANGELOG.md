# Changelog

All notable changes to **WrongTrace** will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [0.2.0] - 2026-08-23

### Added
- **Flagship Ecosystem Integration**: Native deep telemetry, project auto-discovery, and IPC link for **[WrongStack](https://github.com/wrongstack/wrongstack)**.
- **Universal Multi-Agent Ingestion Engine**: Zero-config auto-discovery and log parsing for 20+ contemporary coding agents including:
  - Google Antigravity & Gemini CLI
  - Claude Code (Anthropic)
  - Cursor (Anysphere)
  - Windsurf (Codeium)
  - Cline & Roo Code
  - MiniMax Code (`minimax-text-01`, `abab6.5s`)
  - Kimi Code (`kimi-k2`, `moonshot-v1-128k`)
  - Replit Agent
  - Zed AI
  - ZCode & GLM (Zhipu AI)
  - Devin (Cognition)
  - Trae (ByteDance)
  - Goose (Block / Square)
  - OpenHands (All-Hands AI)
  - GitHub Copilot
  - Aider
  - Continue.dev
- **Multi-Project Workspace Support**: Multi-workspace management with project auto-detection, isolated SQLite databases (`projects.json`), and instant workspace switching in the dashboard.
- **Interactive Code Atlas**: Graph visualization for packages, modules, and file health scores powered by React Flow.
- **Transparent AI Gateway**: Reverse proxy on port `8081` with live token composition tracking, reasoning token (`<think>`) parsing, and prompt cache savings calculation.
- **OpenTelemetry (OTLP) Ingest**: Support for OTLP traces on `/v1/traces`, calculating P50, P90, P99 runtime latency and memory metrics.
- **File Read Heatmap & Token Telemetry**: Line-level file read tracking correlating agent file reads with token spend and cache hit ratios.
- **One-Command Setup CLI (`wrongtrace init`)**:
  - Automatically provisions `.mcp.json`, `CLAUDE.md`, `AGENTS.md`, `.cursorrules`, and `.git/hooks/post-commit`.
- **Enhanced Model Context Protocol (MCP) Server**:
  - Implemented `check_guardrail` tool returning `allowed`, `health_score`, `is_fragile`, and actionable recommendation.
  - Implemented `lock_file` and `unlock_file` for collaborative multi-agent safety.
  - Implemented `get_file_health_score` and `report_telemetry`.
- **Dynamic Real Model Name Extraction**:
  - Replaced hardcoded fallback model assumptions with dynamic regex and JSON extraction from Antigravity `<USER_SETTINGS_CHANGE>`, subagent tool arguments (`invoke_subagent`), and prompt headers.
- **Live 2025/2026 Model Registry & Pricing Seed**:
  - Synced pricing and context windows for Gemini 3.7 Flash, Gemini 2.5 Pro, Claude 3.7 Sonnet, DeepSeek R1, GPT-4.5 Preview, o3-mini, MiniMax Text-01, Kimi K2, and Qwen 2.5 Coder.
- **Comprehensive HTML Observability Report**:
  - Added standalone dark-themed HTML tables for Model ROI Leaderboard, Latency Hotspots, and Thrashing Nodes via `wrongtrace report --format=html`.

### Changed
- **License**: Transitioned to **Business Source License 1.1 (BUSL-1.1)** — free for personal, internal development, and organizational observability, while reserving commercial hosted SaaS rights for WrongStack.
- **Removed Discontinued Projects**: Cleaned legacy/deprecated agents (Inflection Pi, OpenAI Codex API) in favor of active 2025/2026 tools.
- **Thrashing Window Filter**: Enforced SQL-level 24-hour window filter (`julianday(MAX) - julianday(MIN) <= 1.0`) in `HAVING` clause, preventing old historical churn from starving recent thrash events out of the dashboard.
- **Code Atlas Path Matching**: Added multi-format path matching fallback (`relPath`, `cleanPath`, slash-normalized) for Windows backslash compatibility.
- **CORS Middleware**: Added `PUT` and `DELETE` HTTP methods to server CORS configuration.
- **Daemon Memory Footprint**: Set 256MB memory limit ceiling with automated 2-minute GC recycler for lean long-running daemon execution.

---

## [0.1.4] - 2026-08-22

### Changed
- Migrated frontend toolchain to TypeScript 7.0.2.
- Strengthened file watcher debouncing tests under race condition load.
- Verified executable bits and POSIX runner scripts (`dev.sh`).

---

## [0.1.3] - 2026-08-21

### Changed
- Upgraded dashboard bundler to Vite 8 / Rolldown and Tailwind v4.
- Added cross-platform developer runners (`dev.ps1`, `dev.sh`).

---

## [0.1.2] - 2026-08-20

### Changed
- Upgraded dashboard frontend to React 19.

---

## [0.1.1] - 2026-08-15

### Added
- Multi-platform cross-compilation release workflow on GitHub Actions (Linux, Windows, macOS amd64/arm64).

---

## [0.1.0] - 2026-08-01

### Added
- Initial release of WrongTrace single-binary daemon.
- File system watcher (`fsnotify`) with AST semantic diffing.
- Embedded SQLite analytical database with WAL mode.
- Embedded React dashboard via `//go:embed`.
- Windows Named Pipe and Unix Domain Socket IPC listeners.
- Initial MCP stdio server.
