# Changelog

All notable changes to **WrongTrace** will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [Unreleased]

### Added
- **File History Timeline panel (Code Atlas)**: git-style commit graph under the Atlas map showing every recorded mutation of a file across its lifetime. Bursts of AST events within 120s collapse into commit-like revisions (ADD/MOD/DEL badges, per-revision `+/-` deltas, model attribution, mutated-symbol counts, LOC-after); a chronological `+/-` sparkline gives the whole lifetime at a glance; nodes expand to per-symbol diffs. Vertical (newest-first, git-log style) and horizontal orientations, with a file picker that auto-follows the file selected on the map. Powered by the existing `GET /api/metrics/recent?file_path=` endpoint (≤1000 events) — no new backend surface.
- **`WRONGTRACE_PROXY_LOG` env knob**: set to `0`/`false` to silence `[PROXY]` lifecycle console logging without touching telemetry. Windows console writes are synchronous and can add milliseconds per line to the proxy request path.

### Performance
- **Proxy finalize pipeline moved off the request path**: wire analysis, run correlation, traffic persistence, quota accounting, and response-cache fill now run on a bounded background worker (queue cap 256, drops counted and logged) after the response bytes have been written and flushed to the client. The request path no longer decodes the full `messages` array (only `model`/`stream` — last-user-message intent is parsed once in the background) and no longer hashes the request body for cache keys unless the response cache is actually opted in. Server shutdown drains the queue so tail telemetry is not lost on restart.

---

## [0.3.9] - 2026-08-26

### Added
- **Deep Subagent Transcript Discovery (`WRONGTRACE_MAX_SCAN_DEPTH`)**:
  - Raised the transcript scan depth bound from five to eight directories below each watched root. WrongStack nests subagent transcripts at `sessions/<date>/<session>/subagents/<date>/<session>/` — six levels — which the old bound silently dropped entirely.
  - New env knob `WRONGTRACE_MAX_SCAN_DEPTH` (2–16) overrides the bound; dormant-directory tiering keeps deeper traversal near-free unless transcripts actually live there.
  - Measured worst case — every level-six directory freshly active, no tiering skip granted: ~18 ms per steady-state poll over a 40-project fixture, against a 25-second tick.
- **Optional Token Authentication (`WRONGTRACE_TOKEN`)**:
  - When set, every data-bearing surface (`/api/*` including WebSocket, `/proxy/*`, `/v1/traces`) requires `Authorization: Bearer <token>`, `X-WrongTrace-Token`, or `?token=`; comparisons are constant-time.
  - `GET /auth?token=<token>` mints an HttpOnly, SameSite=Lax session cookie backed by a per-process random nonce, so the browser dashboard authenticates without headers. `/api/health` and static dashboard files stay open for liveness probes and SPA loading.
  - Binding a non-loopback interface without a token logs a prominent startup warning instead of failing silently open.
- **Webhook HMAC Signatures (`WRONGTRACE_WEBHOOK_SECRET`)**:
  - Generic webhook deliveries now carry `X-WrongTrace-Signature: sha256=<hex HMAC-SHA256>` over the exact request body so receivers can verify authenticity.

### Fixed & Hardened
- **Silent Migration Failures Surfaced**:
  - `SwitchActiveProject` no longer swaps in a store that failed to open or migrate; it logs the failure and keeps serving the previous database rather than a half-applied schema.
  - Per-project registration (`AddProject`) and the offline `trace` command log `db.Open`, `Migrate`, and insert failures that were previously discarded with `_ =`.
- **Workspace Root Validation**:
  - Project registration refuses filesystem volume roots (`C:\`, `/`) and WrongTrace's own per-project database tree, preventing accidental full-volume indexing and ingest feedback loops.

### Changed
- Removed the dead `var _ = time.Second` import guard from the MCP server; deduplicated `.gitignore`.

---

## [0.3.8] - 2026-08-26

### Performance

CPU-bound and memory-bound work in the long-running daemon was profiled rather
than guessed at. Steady-state CPU was dominated by the transcript poll loop
re-enumerating every watched agent directory on every tick; resident memory was
dominated by the AST snapshot cache pinning the full source text of every
indexed file for the lifetime of the process. Both are addressed at the source.

- **Dormant-Directory Tiered Transcript Scanning**:
  - Replaced the `filepath.WalkDir` poll with a purpose-built traversal that reads directories unsorted, classifies entries by name before assembling a path, and takes file size/mtime from the directory entry instead of a separate `stat`.
  - Added per-directory tiering: a directory whose own mtime is unchanged and whose newest transcript has been untouched for 24 hours is skipped after a single `stat` instead of being fully enumerated, with a forced re-read every 20 polls. Creating, renaming, or removing an entry moves its directory's mtime, so discovery of new sessions keeps full cadence — only an in-place append to a day-old transcript is deferred.
  - Measured on a real workstation tree (28,879 files across 9,611 directories): steady-state poll **~260 ms → ~110 ms**, allocations **1.70 MB → 744 KB** per poll. Ingest output is byte-for-byte unchanged (verified over 8 polls: identical tool calls, read events, sessions, files parsed, and bytes parsed).
- **Compressed, Budgeted AST Source Cache**:
  - Cached file source is now DEFLATE-compressed and governed by a byte budget with LRU eviction (`WRONGTRACE_AST_CACHE_MB`, default 48 MiB; `0` disables source retention entirely).
  - Eviction is graceful: a snapshot that loses its source keeps its node map, so signature- and body-hash-level diffing stays exact and only the line-level `diff_snippet` degrades for long-cold files.
  - `ast.Diff` inflates each side at most once per call rather than per node.
  - Measured on this repository: retained source **4.47 MiB → 1.42 MiB**.
- **Paced Workspace Indexing**:
  - `PrimeDirectory` is bounded to a share of one core (`WRONGTRACE_INDEX_CPU`, default 50) by a duty-cycle pacer that actually sleeps. The previous `runtime.Gosched()` only offered the scheduler a switch and left a free core fully saturated during cold start.
  - The parse arena is returned to the OS once indexing completes.
- **Runtime Footprint Controls**:
  - `GOMAXPROCS` is capped at 4, `GOGC` lowered to 50, and the soft memory limit reduced from 1 GiB to 512 MiB. All three are overridable via `WRONGTRACE_MAX_PROCS`, `WRONGTRACE_GC_PERCENT`, and `WRONGTRACE_MEMORY_LIMIT_MB`.
  - Added a low-frequency idle scavenger that returns unused heap spans to the OS after bursts, skipped entirely unless there is a meaningful amount to reclaim.
  - Measured in isolation (same binary, tuning knobs on vs. off): peak RSS **71.2 MB → 57.0 MB**.
- **Memoized Watcher Ignore Decisions**:
  - `watcher.pathIgnored` results are cached in a bounded map. An editor save-burst or a dependency install replays the same paths through the filter thousands of times, and each miss previously cost four string allocations plus a `filepath.Match` against every `.gitignore` line.
  - `core.isIgnoredDir` no longer lowercases the settings pattern list once per directory judged; the lowercased set is built once and revalidated against the live patterns, so direct settings writes cannot serve a stale ignore list.
- **SQLite Pool Right-Sizing**:
  - `cache_size` is per connection, so the pool multiplied it. Reduced from 8 MiB across up to eight connections to 4 MiB across at most four, with `mmap_size` halved to 32 MiB and idle connections capped at two with a 2-minute idle timeout.
- **Adaptive Dashboard Refresh Coalescing**:
  - The WebSocket invalidation window now widens from 250 ms to 1 s under sustained traffic. A busy agent run previously triggered four full refresh storms per second, each re-running roughly eight queries and re-rendering the dashboard.
  - A hidden tab performs no fetches at all and issues a single catch-up refresh when brought forward.

- **Bounded In-Memory Retention Across Long-Lived Caches**:
  - `BumpCacheGen` now drops the Atlas, metrics, and recent-event maps outright instead of leaving multi-megabyte stale payloads resident until each key happens to be requested again.
  - The model alias cache is capped at 2,048 entries. Model IDs arrive from proxy traffic and can be attacker-controlled, so every version-stamped spelling was previously retained for the lifetime of the daemon.
  - The session-scan cache gained an explicit TTL constant and a 128-entry ceiling with eviction.
  - `SyncModelsDev` reads through a 64 MiB bounded reader instead of an unbounded `io.ReadAll`.
- **Bounded Diff Snippets**:
  - Persisted `diff_snippet` payloads are built through a bounded builder capped at 64 KiB with 3 lines of context, so a single large-file rewrite can no longer write an unbounded blob per event.
- **Ring-Buffered SSE Tail Capture**:
  - The streaming tail sample is retained in a fixed ring buffer that overwrites the oldest bytes, replacing a per-chunk re-shift of the entire tail and linearizing once when stream analysis begins.
- **Metadata-Only IPC Traffic Listing**:
  - `GET /api/ipc/traffic?detail=false` returns summary rows; `GET /api/ipc/traffic/{id}` fetches one bounded request/response pair on demand. The dashboard inspector now lists metadata and loads payloads only for the row a user expands.
  - Retained IPC records compact oversized JSON-RPC values into a scalar summary with an explicit byte count instead of pinning up to the protocol's 16 MiB frame limit.
- **Bounded Webhook Delivery Concurrency**:
  - Alert dispatch is capped at 16 concurrent deliveries and drops beyond that. A broken or slow endpoint could previously turn a burst of guardrail checks into an unbounded goroutine and request-body backlog.
- **HTTP Server Limits**:
  - Added an explicit 90-second idle timeout and a 1 MiB header ceiling to the embedded server.

### Added
- `GET /api/ipc/traffic/{id}` returns a single bounded IPC request/response record; `GET /api/ipc/traffic?detail=false` returns metadata-only rows.
- `ast.Engine.CachedSourceBytes` reports the compressed source currently retained across all snapshots.
- Regression tests covering append detection under directory-timestamp pruning, transcript discovery in already-walked directories, source round-trip through compression, LRU eviction order, and budget accounting across repeated rewrites.
- `BenchmarkPollOnce_SteadyState` and `BenchmarkWalkDirBaseline` pin the steady-state poll cost against a bare directory walk over the same tree.

### Known Limitations
- Transcripts nested more than five directories below a watched root are not ingested. On a WrongStack workspace this excludes `sessions/<date>/<session>/subagents/<date>/<session>/*.jsonl`. This bound predates this release and is unchanged by it; raising it increases scan cost proportionally.

---

## [0.3.7] - 2026-08-25

### Added
- **Confidence-Aware AI Code Attribution**:
  - Added path-scoped tool-operation correlation with persisted attribution source and confidence fields.
  - Concurrent agents targeting the same file now remain unattributed instead of assigning authorship to the last observed run.
- **Durable Incremental Transcript Cursors**:
  - Persisted JSONL offsets across daemon restarts, coalesced checkpoint writes, and forced a final checkpoint during graceful shutdown.

### Changed
- **Byte-Preserving Gateway Observation**:
  - Proxy observation is non-mutating by default. Secret redaction, quota blocking, OpenAI usage injection, and terminal-marker repair require `X-WrongTrace-Policy: enforce`.
  - Exact response caching is explicit via `X-WrongTrace-Cache: allow` and isolated by credential, project, agent, session, run, and query-auth scope.
- **Event-Driven Dashboard Refresh**:
  - Replaced high-frequency API polling with targeted WebSocket cache invalidation, retaining only slow safety polls for time-decaying health snapshots.
  - Lazy-loaded inactive Atlas, diff, session, profiler, gateway, and settings views; React Flow no longer participates in initial dashboard preload.
- **Truthful Model Semantics**:
  - Unknown models remain `unknown-model`; low-confidence attribution is excluded from inter-model friction analytics.

### Fixed
- **AST Identity and TypeScript Parsing**:
  - Switched TypeScript/TSX parsing to the native TSX grammar and qualified class methods to prevent same-name symbol collisions.
- **Streaming and Cache Telemetry**:
  - Retained final usage events from long SSE streams with bounded memory, measured full stream duration, preserved unique cache-hit traffic IDs, and stopped marking truncated streams as complete.
  - Bounded non-stream responses and sanitized stored prompt/header telemetry without mutating transparently relayed bytes.
- **Gateway Route Determinism**:
  - Most-specific dynamic routes now win consistently; route and settings files use owner-only permissions where supported.

### Security
- Rejected foreign browser origins before handlers execute, closing browser-driven CSRF/SSRF access to local proxy routes.
- Prevented response-cache reuse across credentials and query-authenticated providers, and masked secrets in stored telemetry and upstream URL headers.

### Performance
- Removed forced 15-minute memory scavenges and enabled the existing retention setting as low-frequency daily maintenance.
- Reduced the initial dashboard application chunk from 311.8 kB to 96.4 kB and removed React Flow from initial preload.
- Verified steady-state transcript polling at approximately 0.97 ms per 25-second interval on the benchmark fixture.

---

## [0.3.6] - 2026-08-25

### Added
- **Opt-in pprof Debug Profiler Listener**:
  - Integrated `server.StartDebugServer` with loopback pprof endpoints triggered via `WRONGTRACE_PPROF=1` (customizable via `WRONGTRACE_PPROF_ADDR`), keeping unauthenticated public API routes strictly free of profiling endpoints.
- **Cursor-Based Incremental Metrics Streaming**:
  - Implemented cursor pagination in React query `useRecentEvents` using `since` timestamps and client-side merge deduplication, reducing background serialization load from 500 rows to delta increments per poll.
  - Added in-memory generation & TTL cached results for `Engine.GetRecentEventsFiltered` (`recentCache`).

### Optimized & Hardened
- **Zero-Allocation String & Datetime Processing**:
  - Handled fixed SQLite datetime (`len=19`) and RFC3339 (`len=20`) layouts via instant zero-allocation integer parsing in `parseDBTime`.
  - Replaced runtime dynamic slice allocations in `resolvePackageScope` and `SessionWatcher` directory parsing with zero-allocation index splitting.
  - Pre-allocated static byte patterns across `internal/proxy/scanner.go` and `internal/ingest/analyzer.go` to eliminate runtime byte slice conversion allocations.
  - Added O(1) hash map `alwaysIgnoredMap` for rapid path ignore checks during recursive project traversals.
- **Gateway Proxy Streaming Assertion**:
  - Verified true incremental SSE stream chunk delivery before upstream completion with dedicated integration testing.
- **Dashboard UI & Shell Integration**:
  - Restored full dark mode application layout and CSS imports in embedded distribution bundle (`web/dist/index.html`).

---

## [0.3.5] - 2026-08-25

### Fixed & Hardened (Ingest Resiliency, Concurrency, Security & High-Performance CPU Optimization)
- **High-Efficiency AST Diffing & No-Op Save Bypass**:
  - Implemented 0ns hash-equality bypass (`prev.Hash == next.Hash`) in `ast.Diff`, eliminating redundant line-diff splits and LCS matrix calculations for unchanged file states.
  - Added fast SHA-256 pre-check in `Engine.HandleFileChange` to skip full Tree-sitter AST parsing and AST diffing completely when files are touched without content modifications.
- **Session Transcript Watcher & Re-Parse Cascade Prevention**:
  - Replaced random eviction in `SessionWatcher.PollOnce` with structured `fileState` tracking (`offset`, `size`, `modTime`), eliminating catastrophic polling cascades where thousands of transcript files were re-read and parsed from offset 0.
- **Zero-Allocation Rune Truncation & String Slicing**:
  - Refactored `runeSafeTruncate` in both `internal/ingest` and `internal/proxy` to use UTF-8 range index counting, eliminating large `[]rune` array heap allocations on prompt and transcript inspection paths.
- **Precomputed Symbol Signatures & Parser Throughput**:
  - Cached lexical symbol signatures inside `FileSnapshot.sortedSigs` during snapshot creation, converting repeated signature sorting into instant O(1) slice access across Code Atlas and AST diffing.
  - Added snapshot hash verification to `PrimeDirectory` to skip Tree-sitter grammar parsing for already-indexed files.
- **Fast-Path SQLite Datetime Parsing**:
  - Optimized `parseDBTime` with length-based branch switching for standard SQLite datetime (`len=19`) and RFC3339 (`len=20`), avoiding layout array scans across high-volume analytical records.
- **Ingest: Read Events Deduplication & Mid-Write Resiliency**:
  - Derived `ReadID` from session directory + line byte offset to eliminate primary key collisions on `file_read_events`.
  - Incremental JSONL parser now only commits offsets through the last newline, preventing event loss on partial mid-write reads.
- **Proxy: Streaming Cache & Body Validation**:
  - Unified cache lookup and store keys on the final forwarded body to ensure cache hits for streaming traffic.
  - Rejected bodies over 32 MiB with `413 Request Entity Too Large` instead of silent truncation.
  - Streamlined SSE stream handling by removing redundant multi-megabyte JSON unmarshaling attempts on raw event-stream buffers.
- **Security & Concurrency Hardening**:
  - Daemon binds `127.0.0.1` by default with loopback CORS and origin-restricted WebSockets to prevent telemetry exfiltration.
  - Enforced single-instance daemon exclusivity via OS-level `LockFileEx` (Windows) / `flock` (POSIX).
  - Snapshot active repo name under `lockMu` to eliminate data races during project switching.
  - Moved filesystem directory scans outside `lockMu` in project operations (`UpdateProject`, `RescanProject`, `RescanAllProjects`) to eliminate latency stalls on synchronous IPC guardrails.
  - Fixed database migration error handling and atomic multi-table deletions in `ClearStale`.
  - Widened profiler trace IDs to 128 bits to eliminate primary key collisions.

### Changed
- `go.mod`: Cleaned and verified direct dependency labels.

---

## [0.3.4] - 2026-08-25

### Fixed & Hardened (Full-Stack Concurrency, Memory & Performance)
- **High-Efficiency AST Diffing & No-Op Save Bypass**:
  - Implemented 0ns hash-equality bypass (`prev.Hash == next.Hash`) in `ast.Diff`, eliminating redundant line-diff splits and LCS matrix calculations for unchanged file states.
  - Added fast SHA-256 pre-check in `Engine.HandleFileChange` to skip full Tree-sitter AST parsing and AST diffing completely when files are touched without content modifications.
- **Session Transcript Watcher & Re-Parse Cascade Prevention**:
  - Replaced random eviction in `SessionWatcher.PollOnce` with structured `fileState` tracking (`offset`, `size`, `modTime`), eliminating catastrophic polling cascades where thousands of transcript files were re-read and parsed from offset 0.
- **Zero-Allocation Rune Truncation & String Slicing**:
  - Refactored `runeSafeTruncate` in both `internal/ingest` and `internal/proxy` to use UTF-8 range index counting, eliminating large `[]rune` array heap allocations on prompt and transcript inspection paths.
- **Precomputed Symbol Signatures & Parser Throughput**:
  - Cached lexical symbol signatures inside `FileSnapshot.sortedSigs` during snapshot creation, converting repeated signature sorting into instant O(1) slice access across Code Atlas and AST diffing.
  - Added snapshot hash verification to `PrimeDirectory` to skip Tree-sitter grammar parsing for already-indexed files.
- **Fast-Path SQLite Datetime Parsing**:
  - Optimized `parseDBTime` with length-based branch switching for standard SQLite datetime (`len=19`) and RFC3339 (`len=20`), avoiding layout array scans across high-volume analytical records.
- **Thread-Safe Store Access & Race Condition Elimination**:
  - Replaced direct `e.cfg.Store` accesses in `ReportRun`, `VacuumDB`, and `ClearStale` with thread-safe `e.Store()` accessor, eliminating data race conditions and nil pointer dereference risks during concurrent project switching (`SwitchActiveProject`).
- **Guardrail Lock Contention & Deadlock Prevention**:
  - Implemented `isFileLockedUnlocked` internal lock evaluation to avoid duplicate lock acquisitions and eliminate lock contention between `CheckGuardrail` and `FileHealth`.
- **Global Quota Limiter Budget Enforcement**:
  - Fixed budget tracking in `QuotaLimiter` (`CheckSpend`, `CheckAndRecordSpend`, and `RecordSpend`) to accurately account for the `"global"` daily spend limit across distinct agents and projects without isolation leaks.
- **AI Gateway Cache Savings Accounting & Streaming Optimization**:
  - Fixed `ResponseCache.Stats()` to accumulate and accurately report `totalSaved` USD upon LRU cache hits.
  - Streamlined SSE stream handling in gateway proxy by removing redundant multi-megabyte JSON unmarshaling attempts on raw event-stream buffers.
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
