# WrongTrace API Reference

WrongTrace exposes bidirectional interfaces for AI agent telemetry collection, AST-level code observability, file guardrails, and real-time streaming dashboards.

---

## 1. Core Endpoints Overview

| Method | Path | Description |
| :--- | :--- | :--- |
| `GET` | `/api/health` | Daemon status readiness probe, active workspace, WebSocket client count, IPC socket path. |
| `POST` | `/api/telemetry` | Ingest agent run telemetry (tokens, cost, intent, model, provider). |
| `GET` | `/api/events/recent` | Time-ordered AST mutation & code diff event stream (`limit`, `since`, `repo`, `file_path`). |
| `GET` | `/api/cross-thrash` | Inter-agent model friction, collision matrix, and cross-model code overwrites. |
| `GET` | `/api/file/health` | File health score (0-100), fragility status, thrash count, and guardrail lock status. |
| `GET` | `/api/guardrail/check` | Pre-flight safety check for AI agent file modifications. |
| `GET` | `/api/guardrail/locks` | List active, unexpired guardrail file locks with TTL metadata. |
| `POST` | `/api/guardrail/lock` | Lock a file against automated agent changes (`owner`, `ttl_minutes`, `reason`). |
| `POST` | `/api/guardrail/unlock` | Release a guardrail lock on a file. |
| `GET` | `/api/symbol/history` | Chronological evolution and model authorship lineage of an AST symbol or entire file. |
| `GET` | `/api/files/activity` | Per-model read vs write activity breakdown for a file or all monitored files. |
| `GET` | `/api/atlas` | Code Atlas graph (packages, files, AST symbols) with scoping, summary mode, and pagination. |
| `GET` | `/api/atlas/status` | Current progress of background code graph indexing. |
| `GET` | `/api/metrics/overview` | High-level metrics: files observed, thrash count, token costs, active projects. |
| `GET` | `/api/metrics/thrashing` | Files and AST nodes with highest thrashing / lowest survival rates. |
| `GET` | `/api/metrics/models` | Efficiency, total token spending, code churn by LLM model. |
| `GET` | `/api/reads/recent` | Real-time file read events captured by agent inspection tools. |
| `GET` | `/api/files/reads` | Most frequently read files and token costs. |
| `GET` | `/api/files/heatmap` | Line-by-line read frequency heatmap for a file. |
| `GET` | `/api/proxy/routes` | Active AI reverse proxy routes. |
| `POST` | `/api/proxy/routes` | Register or update upstream AI proxy route. |
| `DELETE` | `/api/proxy/routes/:id` | Remove a proxy route. |
| `GET` | `/api/proxy/traffic` | Intercepted AI completions, tokens, latency, cost. |
| `GET` | `/api/projects` | Multi-project workspace profiles and status. |
| `POST` | `/api/projects` | Register a new project workspace. |
| `GET` | `/api/models/catalog` | Dynamic pricing, provider, and context window registry. |
| `POST` | `/api/profiler/ingest` | Universal profiler test runner / CLI execution trace ingest. |
| `POST` | `/api/profiler/otlp/v1/traces` | Standard OpenTelemetry OTLP trace receiver. |

---

## 2. Endpoint Specifications

### 2.1. `POST /api/telemetry`
Records agent execution metadata at task completion.

* **Request Body:**
```json
{
  "run_id": "run-ws-402",           // Optional: auto-generated if omitted
  "task_id": "TASK-12",             // Required: issue/task reference
  "agent_name": "WrongStack",       // Agent framework / client name
  "model_name": "claude-3-7-sonnet",// Model identifier
  "provider": "anthropic",          // Model provider (anthropic, openai, google, etc.)
  "prompt_tokens": 12000,           // Prompt token count (or tokens_used)
  "completion_tokens": 850,         // Completion token count
  "cost_usd": 0.048,                // Dollar spend (auto-calculated from model catalog if 0)
  "intent": "Refactor auth middleware"
}
```

* **Response (200 OK):**
```json
{
  "ok": true,
  "status": "ok",
  "event_id": "run-ws-402",
  "run_id": "run-ws-402"
}
```

---

### 2.2. `GET /api/events/recent`
Returns time-ordered AST mutation and code diff events.

* **Query Parameters:**
  * `limit` *(int, default: 50)*: Maximum number of events to return.
  * `since` *(string, optional)*: ISO 8601 or RFC 3339 timestamp (e.g. `2026-08-24T20:00:00Z`).
  * `repo` *(string, optional)*: Filter by repository / project name.
  * `file_path` or `path` *(string, optional)*: Filter events for a specific file.

* **Response (200 OK):**
```json
[
  {
    "event_id": "ev-8f2a1b",
    "run_id": "run-ws-402",
    "repo_name": "wrongtrace",
    "file_path": "internal/server/server.go",
    "node_signature": "function:server.go::New",
    "node_type": "function",
    "action": "MODIFIED",
    "author_model": "claude-3-7-sonnet",
    "added_lines": 14,
    "deleted_lines": 2,
    "loc": 45,
    "body_hash": "a1b2c3d4...",
    "timestamp": "2026-08-24T20:45:10Z"
  }
]
```

---

### 2.3. `GET /api/cross-thrash` (Alias: `/api/metrics/cross-thrash`, `/api/metrics/friction`)
Returns inter-agent conflict metrics, overwrite matrices, and cross-thrash events.

* **Query Parameters:**
  * `limit` *(int, default: 200)*: Limit for recent cross-model collisions.

* **Response (200 OK):**
```json
{
  "edges": [
    {
      "author_model": "claude-3-7-sonnet",
      "overwriter_model": "gemini-2.5-flash",
      "conflict_count": 4,
      "lines_modified": 38,
      "lines_deleted": 12,
      "is_self_thrash": false,
      "wasted_cost_usd": 0.084
    }
  ],
  "recent_collisions": [
    {
      "event_id": "ev-c4d5e6",
      "file_path": "internal/core/guardrails.go",
      "node_signature": "function:guardrails.go::CheckGuardrail",
      "action": "MODIFIED",
      "author_model": "claude-3-7-sonnet",
      "overwriter_model": "gemini-2.5-flash",
      "is_cross_agent": true,
      "occurred_at": "2026-08-24T20:50:00Z"
    }
  ],
  "summary": {
    "total_conflicts": 4,
    "cross_agent_conflicts": 4,
    "cross_agent_ratio_pct": 100.0,
    "total_wasted_cost_usd": 0.084,
    "most_conflicted_file": "internal/core/guardrails.go",
    "most_aggressive_model": "gemini-2.5-flash"
  }
}
```

---

### 2.4. `POST /api/guardrail/lock` & `GET /api/guardrail/locks`
Manage guardrail file locks with TTL expiration, owner tracking, and conflict detection.

* **POST Request Body:**
```json
{
  "path": "internal/server/server.go",  // Or "file_path"
  "reason": "WrongStack refactor in progress",
  "owner": "WrongStack (claude-3-7-sonnet)",
  "owner_run_id": "run-ws-402",
  "ttl_minutes": 15,                    // Optional: default 15 minutes (or "ttl_seconds": 900, "ttl": "15m")
  "force": false                        // Set true to override an existing lock owned by another agent
}
```

* **POST Success Response (200 OK):**
```json
{
  "ok": true,
  "status": "locked",
  "path": "internal/server/server.go",
  "reason": "WrongStack refactor in progress",
  "owner": "WrongStack (claude-3-7-sonnet)",
  "owner_run_id": "run-ws-402",
  "locked_at": "2026-08-24T21:00:00Z",
  "expires_at": "2026-08-24T21:15:00Z"
}
```

* **POST Conflict Response (409 Conflict):**
Returned when the file is already actively locked by another owner and `force: true` was not specified:
```json
{
  "ok": false,
  "status": "conflict",
  "error": "file is already locked",
  "message": "file internal/server/server.go is already locked by Devin (claude-3-7-sonnet)",
  "path": "internal/server/server.go",
  "reason": "critical AST refactoring",
  "owner": "Devin (claude-3-7-sonnet)",
  "owner_run_id": "run-devin-101",
  "locked_at": "2026-08-24T20:55:00Z",
  "expires_at": "2026-08-24T21:10:00Z"
}
```

* **GET `/api/guardrail/locks` Response (200 OK):**
```json
[
  {
    "path": "internal/server/server.go",
    "reason": "WrongStack refactor in progress",
    "owner": "WrongStack (claude-3-7-sonnet)",
    "owner_run_id": "run-ws-402",
    "locked_at": "2026-08-24T21:00:00Z",
    "expires_at": "2026-08-24T21:15:00Z"
  }
]
```

---

### 2.5. `GET /api/file/health`
Returns the file health score, fragility status, thrash count, and guardrail lock status.

* **Query Parameters:**
  * `path` or `file_path` *(string, required)*: Path to file.

* **File Health Scoring Formula:**
  $$\text{HealthScore} = \max\left(0, 100 - (\text{RecentThrashingCount} \times 15) - (\text{ChurnCount} \times 2)\right)$$
  * **Fragility Threshold**: `HealthScore < 40` or `RecentThrashingCount >= 3` marks `is_fragile: true`.
  * **Recommendation**:
    * Score $\ge 70$: Safe to modify.
    * $40 \le \text{Score} < 70$: Caution: Apply minimal localized diffs.
    * Score $< 40$ or Fragile: Modification discouraged without human review.
    * Locked: Modification blocked by guardrail.

* **Response (200 OK):**
```json
{
  "file_path": "internal/server/server.go",
  "health_score": 85,
  "is_fragile": false,
  "recent_thrashing_count": 1,
  "is_locked": true,
  "lock_reason": "WrongStack refactor in progress",
  "lock_owner": "WrongStack (claude-3-7-sonnet)",
  "lock_owner_run_id": "run-ws-402",
  "lock_expires_at": "2026-08-24T21:15:00Z",
  "warning": ""
}
```

---

### 2.6. `GET /api/symbol/history` (Aliases: `/api/symbols/history`, `/api/node/history`, `/api/nodes/history`)
Queries the chronological modification lineage for an AST symbol or all symbols in a file.

* **Query Parameters:**
  * `file_path` or `path` *(string, optional)*: Path to source file (relative, absolute, or partial).
  * `signature` or `name` or `symbol` *(string, optional)*: Symbol identifier or free name.
    * **Free name querying**: `?signature=New`, `?name=New`, `?signature=New()`, `?signature=Alfa(x, y int)`.
    * **Full signature format**: `kind:filename::SymbolName` (e.g. `function:server.go::New`, `method:engine.go::Engine.LockFile`).
    * **Supported AST Kinds**: `function`, `method`, `class`, `struct`, `interface`, `arrow_function`, `type`, `variable`.
    * **File-Wide Mode**: When signature is omitted (`?file_path=internal/server/server.go`), returns the history of all AST symbols in that file.
  * `limit` *(int, default: 100)*: Maximum history records.

---

### 2.7. `GET /api/atlas`
Returns the semantic codebase graph with optional workspace filtering, prefix scoping, summary mode, symbol exclusion, and pagination.

* **Query Parameters:**
  * `workspace` *(string, optional)*: Filter packages by workspace / repository name.
  * `prefix` *(string, optional)*: Filter packages/files starting with a path prefix (e.g. `internal/core`).
  * `summary` *(bool, optional)*: When `true` (or `mode=summary`), returns packages without file arrays, providing `file_count`, `fragile_files_count`, `avg_health_score`, `total_loc`, and `is_fragile`.
  * `include_symbols` *(bool, optional, default: true)*: When `false` (or `symbols=false`), retains `files` with individual file health scores but strips heavy AST symbol trees.
  * `limit` *(int, optional)*: Number of packages per page.
  * `offset` *(int, optional)*: Package offset for pagination.

* **Response Structure:**
```json
{
  "repo": "wrongtrace",
  "generated_at": "2026-08-24T21:40:00Z",
  "is_monorepo": false,
  "total_packages": 12,
  "limit": 5,
  "offset": 0,
  "total_files": 48,
  "total_loc": 14200,
  "total_nodes": 450,
  "packages": [
    {
      "path": "internal/core",
      "name": "core",
      "workspace": "internal",
      "file_count": 8,
      "fragile_files_count": 0,
      "avg_health_score": 92.5,
      "total_loc": 3200,
      "is_fragile": false,
      "files": [ ... ]             // omitted when summary=true
    }
  ]
}
```

---

### 2.8. `GET /api/files/activity`
Returns per-model read vs write activity summary sorted chronologically by most recent activity (`last_activity_at DESC`).

* **Query Parameters:**
  * `file_path` *(string, official primary; `path` and `file` accepted as aliases)*: When provided, returns activity for the specific file. When omitted, returns aggregated activity across all monitored files.

---

## 3. Error Handling Format

All HTTP error responses return a standardized JSON structure with both `error` and `message` keys:

```json
{
  "error": "path query parameter is required",
  "message": "path query parameter is required"
}
```

---

## 4. WebSocket Real-Time Feed (`ws://localhost:8000/api/ws`)

Upon connecting, the server broadcasts live JSON messages for every observed event:

### `code_event` (AST Mutation)
```json
{
  "type": "code_event",
  "data": {
    "id": 402,
    "event_time": "2026-08-22T20:10:00Z",
    "file_path": "internal/db/queries.go",
    "node_type": "func",
    "node_name": "Thrashing",
    "action": "MODIFIED",
    "loc_delta": 4,
    "diff_content": "@@ -120,3 +120,7 @@ ...",
    "agent_name": "Antigravity",
    "model_name": "gemini-3.7-flash"
  }
}
```

### `run_reported` (Agent Telemetry)
```json
{
  "type": "run_reported",
  "data": {
    "run_id": "run-ws-402",
    "task_id": "TASK-12",
    "agent_name": "WrongStack",
    "model_name": "claude-3-7-sonnet",
    "prompt_tokens": 12000,
    "completion_tokens": 850,
    "cost_usd": 0.048,
    "intent": "Refactor auth middleware"
  }
}
```

---

## 5. Model Context Protocol (MCP) Server

WrongTrace includes an embedded MCP server (`wrongtrace mcp`) communicating over `stdin`/`stdout` JSON-RPC 2.0.

### Available Tools:
1. **`report_telemetry`**: `{ model/model_name, provider, task_id, intent, prompt_tokens, completion_tokens, cost_usd }`
2. **`check_guardrail`**: `{ file_path }` → `{ allowed: bool, health_score: int, is_fragile: bool, is_locked: bool, lock_reason, lock_owner, recommendation }`
3. **`get_file_health_score`**: `{ file_path }` → `{ health_score: int, is_fragile: bool, recent_thrashing_count: int, is_locked: bool }`
4. **`lock_file`**: `{ file_path, reason, owner, owner_run_id, ttl_minutes, ttl_seconds }`
5. **`unlock_file`**: `{ file_path }`
6. **`list_locks`**: `{ filter?: string }`
7. **`report_file_read`**: `{ file_path, model, provider, tool_name, start_line, end_line, prompt_tokens, cost, intent }`
8. **`get_file_read_stats`**: `{ file_path }`
9. **`get_file_diff_history`**: `{ file_path, limit }`

---

## 6. Local IPC Protocol (Named Pipe / Unix Socket)

WrongTrace provides high-throughput, zero-latency local IPC:
* **Windows Named Pipe:** `\\.\pipe\wrongtrace`
* **Linux/macOS Unix Domain Socket:** `~/.wrongtrace/wrongtrace.sock`

### IPC Methods:
* `telemetry/report_run`: `{ run_id, task_id, agent_name, model_name, provider, prompt_tokens, completion_tokens, cost_usd, intent }`
* `telemetry/file_health`: `{ file_path }` → returns `FileHealthReply` with `health_score`, `is_fragile`, `is_locked`, `lock_reason`, `lock_owner`, `lock_expires_at`.

