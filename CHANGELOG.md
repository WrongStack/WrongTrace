# Changelog

All notable changes to **WrongTrace** will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [Unreleased]

### Fixed
- **Ingest: read events silently dropped** — `ReadID` was derived from a per-batch counter over a base-name-only session ID, so every poll after the first (and every session sharing the file name `transcript.jsonl`) collided on the `file_read_events` primary key and was discarded via `ON CONFLICT DO NOTHING`. IDs are now derived from the session directory + line byte offset (stable across re-reads, unique per session), and session IDs include the parent directory so distinct agent sessions no longer merge into one `agent_runs` row.
- **Ingest: events lost on mid-write polls** — the incremental JSONL parser committed its offset past an unterminated (partially written) trailing line, permanently losing every event on it. The parser now only advances the committed offset through the last newline; an unterminated tail is parsed best-effort but re-read on the next poll once complete.
- **Proxy: response cache never hit for streaming traffic** — the cache lookup key was computed before `stream_options.include_usage` was injected into the request body, while the store key was computed after (and with the analysis-rewritten model), so stored entries were unreachable. The key is now computed once on the final forwarded body and reused for both lookup and store.
- **Proxy: oversized bodies silently truncated** — request bodies over 32 MiB were truncated by `io.LimitReader` and forwarded upstream as corrupt JSON; they are now rejected with `413 Request Entity Too Large`.
- **Server: daemon bound all interfaces with `*` CORS and unrestricted WebSocket origins** — an unauthenticated, single-user daemon was readable from any webpage (telemetry exfiltration) and from the LAN. It now binds `127.0.0.1` by default (`--bind 0.0.0.0` restores the old behavior), CORS is limited to loopback origins, and WebSocket upgrades reject non-loopback origins. The WebSocket greeting write also gained the deadline every other write already had, so a stalled client cannot pin the handler goroutine.
- **Lock: single-instance enforcement was check-then-act only** — no OS-level lock was ever taken, so two daemons started concurrently (or on different ports) both ran, corrupting PID files and double-ingesting. `daemon.lock` is now held via `LockFileEx` (Windows) / `flock` (POSIX), released automatically on crash; the lock file is no longer unlinked on release (inode race).
- **Core: data race on active repo name** — `Engine.cfg.RepoName` was written under `lockMu` during project switches but read unlocked on watcher/ingest/HTTP paths; all reads now snapshot it under the same mutex.
- **Core: project operations held `lockMu` across filesystem scans** — `UpdateProject`/`RescanProject`/`RescanAllProjects` walked agent session directories (seconds on Windows) while holding the lock that synchronous IPC guardrail checks need; scans now run outside the lock.
- **Core: concurrent `AddProject` both marked active** — the `isFirst` decision moved under `lockMu`.
- **DB: `Migrate` swallowed real schema errors** — `ALTER TABLE`/`CREATE INDEX` failures (locked DB, full disk) returned success and surfaced later on every insert; only the expected "duplicate column"/"already exists" no-ops are tolerated now.
- **DB: `ClearStale` pruned partially on failure** — the four per-table DELETEs now run in a single transaction.
- **Profiler: 32-bit trace IDs collided on the primary key** — IDs widened to 128 bits; swallowed `InsertTrace` errors are now logged.
- **CLI: `trace` ignored `WRONGTRACE_PORT`/`PORT`** — port resolution now matches `start`, so traces reach the running daemon instead of silently writing to the default database.

### Changed
- `go.mod`: direct dependencies are no longer mislabeled `// indirect` (ran `go mod tidy`).

---

## [0.3.4] - 2026-08-25

### Fixed & Hardened (Full-Stack Concurrency, Memory & Performance)
- **Thread-Safe Store Access & Race Condition Elimination**:
  - Replaced direct `e.cfg.Store` accesses in `ReportRun`, `VacuumDB`, and `ClearStale` with thread-safe `e.Store()` accessor, eliminating data race conditions and nil pointer dereference risks during concurrent project switching (`SwitchActiveProject`).
- **Guardrail Lock Contention & Deadlock Prevention**:
  - Implemented `isFileLockedUnlocked` internal lock evaluation to avoid duplicate lock acquisitions and eliminate lock contention between `CheckGuardrail` and `FileHealth`.
- **Global Quota Limiter Budget Enforcement**:
  - Fixed budget tracking in `QuotaLimiter` (`CheckSpend`, `CheckAndRecordSpend`, and `RecordSpend`) to accurately account for the `"global"` daily spend limit across distinct agents and projects without isolation leaks.
- **AI Gateway Cache Savings Accounting**:
  - Fixed `ResponseCache.Stats()` to accumulate and accurately report `totalSaved` USD upon LRU cache hits.
- **Secret Scanner Hot-Path CPU Optimization**:
  - Added short-payload fast-path early exit to `ScanAndRedactSecrets` in `internal/proxy/scanner.go`, reducing heap string allocations and CPU overhead during streaming LLM proxying.
- **Dashboard WebSocket Debounce & Memory Leak Fix**:
  - Refactored `useEffect` debounce mechanism in `web/src/pages/Dashboard.tsx` with stable reference map (`refetchMapRef`), eliminating debounce timer leaks, stale closures, and render churn during high-frequency WebSocket event bursts.
- **Comprehensive Test Suite & High Statement Coverage**:
  - Added end-to-end unit and integration tests across CLI commands, JSON-RPC IPC methods, QuotaLimiter, ResponseCache, and Guardrail lifecycles, ensuring reliable stability.

---

## [0.3.3] - 2026-08-24

### Fixed & Hardened (Memory, CPU & System Stability)
- **SQLite Memory Over-Allocation & GC Thrashing Elimination**:
  - Tuned SQLite connection pool pragmas from excessive 64 MiB page cache per connection (`cache_size(-65536)`) and 32 connections (which allocated up to 2 GB RAM, triggering intense Go GC CPU thrashing against the 256MB soft limit) down to a lean 8 MiB per connection (`cache_size(-8192)`), bounded max connection pool (`2..8` connections), and reduced `mmap_size` (64 MiB).
- **Regex Compilation Hoisting & Hot-Path CPU Optimization**:
  - Hoisted all runtime `regexp.MustCompile` calls in hot request/transcript processing paths (`internal/proxy/analyzer.go` and `internal/ingest/analyzer.go`) to package-level singleton variables, eliminating per-request regex compilation overhead and heap allocations.
- **Zero-Creep Ring Buffer Memory Management**:
  - Replaced re-slicing with in-place `copy-shift` ring buffers for IPC traffic (`ipcTraffic`) and gateway wire traffic logs (`trafficLog`), allowing the Go Garbage Collector to immediately release old payload objects from memory.
- **Database Hot-Swap & Project Switching Thread-Safety**:
  - Protected all database store accesses in `Engine`, `Metrics`, `Atlas`, `FileHealth`, and telemetry recording with synchronized `e.Store()` and nil-store guards.
  - Eliminated concurrent query collisions on SQLite during project activation (`SwitchActiveProject`).
- **SSE Stream Wire Parser Crash Protection**:
  - Replaced unsafe type assertions (`chunk["index"].(float64)`, `cb["id"].(string)`) in Anthropic/OpenAI wire payload analyzers (`internal/proxy/analyzer.go`) with safe comma-ok type assertions, preventing nil interface conversion panics.
- **Unbounded Memory & Session Map Leak Prevention**:
  - Added strict capacity ceiling (250 entries) and LRU eviction for in-memory gateway session tracking (`p.sessions`) in `GatewayProxy`.
  - Capped maximum streamed SSE payload capture buffer (`capturedBuffer`) at 1 MB to prevent heap ballooning on massive LLM token generations.
  - Capped proxy request body ingestion at 32 MB to guard against OOM payloads.
- **Query Timeout Guarantees**:
  - Enforced `s.withTimeout()` across analytical queries (`Thrashing`, `ModelComparison`, `FileHealth`, `AllFilesHealth`, `AllNodeStats`, `RecentTraces`, `ProfilerHotspots`, `ProfilerOverview`), eliminating indefinite connection lockups.
- **Full JSON-RPC IPC Method Compatibility & POSIX Resilience**:
  - Implemented complete method coverage and flexible parameter mapping for `telemetry/report_file_read` (`report_file_read`), `check_guardrail` (`guardrail/check`), `report_telemetry`, `lock_file` (`guardrail/lock`), `unlock_file` (`guardrail/unlock`), `list_locks`, `atlas` (`get_atlas`), `get_file_read_stats`, `get_file_diff_history`, and introspection (`rpc.discover`, `system.listMethods`), eliminating all `-32601 method not found` errors from WrongStack, MCP clients, and autonomous agent probes.
  - Added Darwin/Linux `sockaddr_un.sun_path` length bounds (104 bytes on macOS, 108 bytes on Linux) with automatic `/tmp` fallback to prevent socket creation failures on systems with long home/temp directories.
  - Automatically established `/tmp/wrongtrace.sock` symlinks on POSIX for seamless dual-path agent discovery (`~/.wrongtrace/wrongtrace.sock` $\leftrightarrow$ `/tmp/wrongtrace.sock`).

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
