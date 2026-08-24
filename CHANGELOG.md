# Changelog

All notable changes to **WrongTrace** will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [0.3.2] - 2026-08-24

### Fixed
- **Committed Web Dist Placeholder Invariant**:
  - Restored clean placeholder UI in `web/dist/index.html` to satisfy `TestCommittedDistIndexIsPlaceholder` and clean-clone embed guarantees without gitignored assets.
- **CI Dependency Parity**:
  - Synchronized `web/package-lock.json` with `web/package.json` for strict `npm ci` execution.

---

## [0.3.1] - 2026-08-24

### Added
- **Live Windows Named Pipe & IPC Inspector (`\\.\pipe\wrongtrace`)**:
  - Real-time JSON-RPC interaction inspector (`IPCTrafficView.tsx`) directly embedded in the Live Feed and Agent Sessions views, displaying incoming request payloads, returned daemon outputs, duration latency in milliseconds, and one-click JSON clipboard copying.
  - Added `RecordIPCTraffic` on `Engine` with WebSocket streaming (`ipc_traffic`) and `GET /api/ipc/traffic` endpoint.
- **Enhanced Universal Agent Observability & WrongStack API Standards**:
  - `GET /api/symbol/history`: Free-form signature querying (`?signature=foo()`, `?name=foo`), case-insensitive suffix/substring matching, whole-file symbol history mode (`?file_path=...`), and AST kind documentation.
  - `GET /api/events/recent`: Robust `since` timestamp parser supporting ISO 8601, RFC 3339 (`2026-08-24T18:00:00Z`), DateTime, and Unix epoch formats; unforced repo scoping.
  - `GET /api/atlas`: Monorepo payload optimization via `?summary=true` (compact directory metrics without file arrays), `?include_symbols=false` (files with health scores, stripped AST trees), and standard `limit` & `offset` pagination.
  - `GET /api/files/activity`: Official `file_path` primary parameter, sorted by `last_activity_at DESC`.
  - `POST /api/guardrail/lock`: 409 Conflict detection and response when a file is actively locked by another agent/owner; override via `"force": true`.
  - Standardized JSON error response format (`{"error": "...", "message": "..."}`) for 404, 405, and API errors.

### Fixed
- **IPC Client Disconnect Error Log Filtering**:
  - Added `isClientDisconnect` check in socket handler to cleanly recognize benign client connection closes (`The pipe is being closed`, `net.ErrClosed`, `io.EOF`) on Windows named pipes without logging false error messages.
- **Port Stability in Dev Runners**:
  - Fixed port drift in `dev.ps1` and `dev.sh` by binding dashboard to `:8000` and daemon to `:8001` with proper environment cleanup on exit.

---

## [0.3.0] - 2026-08-24

### Added
- **Inter-Agent Friction & Cross-Thrashing Matrix ("Who Broke Whose Code?")**:
  - Implemented deep causality tracking via SQLite window functions (`LAG(...) OVER PARTITION BY file_path, node_signature ORDER BY event_time`) identifying the original author model and subsequent overwriting or deleting models.
  - Interactive **Collision Heatmap Grid** visualizing cross-model overwrite frequencies and deleted LOC volumes.
  - Real-time **Inter-Agent Overwrites Stream** displaying time-delta durations ($\Delta T$), action pills (`MODIFIED` / `DELETED`), and expandable syntax-highlighted Rich Diff code snippets.
  - Multi-mode filtering: All Events, Cross-Model Collisions Only (`⚔️`), and Self-Thrashing Loops (`🔄`).
  - Added `/api/metrics/friction` and `/api/metrics/cross-thrash` endpoints.
- **Next-Gen Code Atlas Architecture**:
  - **Immersive Fullscreen Mode**: Added full-viewport interactive canvas mode with smooth transitions and `Escape` key shortcut.
  - **Intelligent Smooth Auto-Arranger (`FlowAutoArranger`)**: Automatic viewport framing and smooth centering animation (`fitView({ duration: 500, padding: 0.25 })`) across package/file/symbol drilldowns and layout switches (`Orbit`, `Tree`, `Grid`).
  - **Visual Color Palette Modes**: Seamlessly toggle between Hierarchy Palette (architecture-coded), Health Heatmap (score-based), and Churn Velocity.
  - **Global Quick Search Keyboard Shortcut**: `/` and `Ctrl+K` for instant AST symbol filtering.
  - **Tabbed Multi-Inspector Drawer**: Organized into Architecture Metrics, AST Symbol Evolution Timeline, and Context Reads / Model Activity breakdown.
- **Model Intelligence & Code Durability Matrix**:
  - Unified table evaluating AI model performance: Quality Tiers (S/A/B/C), 14-Day Code Survival Rate %, True Token ROI ($/survived node), context read volume, and net code longevity.
- **AST Symbol Evolution & Lineage API**:
  - Added `/api/symbol/history`, `/api/symbols/history`, `/api/node/history`, and `/api/nodes/history` routes providing full chronological revision history for any AST node.
  - Added `/api/files/activity` and `/api/file/activity` for per-model file read vs write volume breakdown.

### Fixed
- **AST Parser Multi-Line Diffing Precision**:
  - Resolved single-line diff counting where `normalizeForHash` collapsed newlines into single spaces before assigning to `Node.Body`. `Node.Body` now preserves original multi-line formatting while computing normalized SHA-256 hashes, producing exact `+AddedLines / -DeletedLines` line counts.
- **Diff Inspector Height & Viewport Layout**:
  - Removed restrictive fixed height boundaries on the diff viewer panel, allowing seamless full-page code expansion with sticky timeline navigation.

---

## [0.2.2] - 2026-08-24

### Changed
- **Unified Port 8000 Architecture**: Migrated default HTTP/API/Dashboard entrypoint to port `8000`. Cleaned up legacy `5173` port references across Vite dev server, dev runners (`dev.ps1`, `dev.sh`), and proxy components.
- **High-Signal Focused Console Logging**: Console output now filters noisy background API/polling requests (`/api/*`, health checks) by default while keeping AI Proxy completions and errors active. Full verbose logging remains accessible via `WRONGTRACE_LOG_ALL_HTTP=1`.

### Added
- **Internal Request ID Tagging (`px-xxxx`)**: Every AI proxy request is assigned a unique internal correlation ID stamped on every log line (`[PROXY] [px-xxxx] -> ...`, `[PROXY] [px-xxxx] <- ...`). The ID matches the persisted SQLite traffic ID and UI record for instant multi-agent correlation.
- **PowerShell Real-time Telemetry Feed**: Integrated a colorized live request and proxy log streamer directly into `dev.ps1`.

---

## [0.2.1] - 2026-08-24

### Fixed
- **Ignore-Rule Scoping (watcher + engine)**: Ignore rules are now matched against paths *relative to* the watched root, in both `watcher.pathIgnored` and `Engine.shouldSkip`. Previously the whole absolute path was inspected, so a checkout living under an ancestor named like an ignore entry (`/tmp/...`, `~/build/...`, `C:\bin\...`) matched at its own root: the watcher registered nothing and the engine dropped every file change, so no code events were ever emitted. `core.Config` gains `WatchDir` (back-filled by `PrimeDirectory`) to carry that root.
- **Guardrail Lock Path Folding**: Lock bookkeeping folded separators with `filepath.ToSlash`, a no-op outside Windows, so a lock taken as `internal\core\engine.go` became one opaque segment that matched nothing, not even itself under a different spelling. Both separator styles are now folded explicitly.
- **Thread-Safe Store Access**: `Engine.Store()` is guarded by an `RWMutex` and `profiler.Collector` reaches the store through a `GetStore` callback, removing the data race between the daemon's rebuild path and metric collection.
- **Quota Check Separation**: Split read-only validation (`QuotaLimiter.CheckSpend`) from the mutating `CheckAndRecordSpend`, so a rejected request no longer consumes budget.
- **Proxy Response Header Ordering**: Response headers are written before body streaming begins, fixing dropped headers on streamed completions.
- **Guardrail Lock Reasons**: `lockedFiles` now carries the lock reason (`map[string]string`) so blocked agents are told *why* a file is locked.
- **MCP Guardrail Lock Enforcement**: Integrated file lock checks directly into MCP `check_guardrail` handler so locked files are blocked for agents using MCP.
- **Dynamic Proxy Route Boundary Matching**: Fixed `MatchRoute` prefix matching to enforce strict path boundaries, preventing false prefix matches like `/proxy/zaix` against `/proxy/zai`.
- **Active Node Resurrection Status**: Fixed SQL queries to accurately compute active node lifecycles when code nodes are deleted and subsequently restored.
- **Wasted Spend Zero-Division Guard**: Safeguarded dashboard metrics against negative or miscalculated wasted spend for read-only / inspection models.
- **Dist Placeholder CI Invariant**: Restored the committed `web/dist/index.html` placeholder UI so fresh clones satisfy `//go:embed all:web/dist` without the gitignored `/assets/*` bundles. `TestCommittedDistIndexIsPlaceholder` now enforces this in CI instead of review discipline.

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
- **Interactive Code Atlas**: Graph visualization for packages, modules, and file health scores.
- **Transparent AI Gateway**: Reverse proxy with live token composition tracking, reasoning token (`<think>`) parsing, prompt cache savings calculation, and boundary-safe route matching.
- **OpenTelemetry (OTLP) Ingest**: Support for OTLP traces on `/v1/traces`, calculating P50, P90, P99 runtime latency and memory metrics.
- **File Read Heatmap & Token Telemetry**: Line-level file read tracking correlating agent file reads with token spend and cache hit ratios (`report_file_read`, `get_file_read_stats`).
- **One-Command Setup CLI (`wrongtrace init`)**:
  - Automatically provisions `.mcp.json`, `CLAUDE.md`, `AGENTS.md`, `.cursorrules`, and `.git/hooks/post-commit`.
- **Enhanced Model Context Protocol (MCP) Server**:
  - Implemented `check_guardrail` tool enforcing file locks, health scores, fragility checks, and recommendations.
  - Implemented `lock_file` and `unlock_file` for collaborative multi-agent safety with case-insensitive and suffix-matched path normalization.
  - Implemented `get_file_health_score`, `report_telemetry`, `report_file_read`, and `get_file_read_stats`.
- **Dynamic Real Model Name Extraction**:
  - Replaced hardcoded fallback model assumptions with dynamic regex and JSON extraction from Antigravity `<USER_SETTINGS_CHANGE>`, subagent tool arguments (`invoke_subagent`), and prompt headers.
- **Live 2025/2026 Model Registry & Pricing Seed**:
  - Synced pricing and context windows for Gemini 3.7 Flash, Gemini 2.5 Pro, Claude 3.7 Sonnet, DeepSeek R1, GPT-4.5 Preview, o3-mini, MiniMax Text-01, Kimi K2, and Qwen 2.5 Coder.
- **Comprehensive HTML Observability Report**:
  - Added standalone dark-themed HTML tables for Model ROI Leaderboard, Latency Hotspots, and Thrashing Nodes via `wrongtrace report --format=html`.

### Fixed
- **MCP Guardrail Lock Enforcement**: Integrated file lock checks directly into MCP `check_guardrail` handler so locked files are blocked for agents using MCP.
- **Dynamic Proxy Route Boundary Matching**: Fixed `MatchRoute` prefix matching to enforce strict path boundaries, preventing false prefix matches like `/proxy/zaix` against `/proxy/zai`.
- **Active Node Resurrection Status**: Fixed SQL queries to accurately compute active node lifecycles when code nodes are deleted and subsequently restored.
- **Wasted Spend Zero-Division Guard**: Safeguarded dashboard metrics against negative or miscalculated wasted spend for read-only / inspection models.

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
