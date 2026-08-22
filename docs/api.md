# WrongTrace API Reference

WrongTrace exposes multiple protocol interfaces for bidirectional AI agent communication, live telemetry streaming, and observability dashboards.

---

## 1. REST API (`http://localhost:4318`)

| Endpoint | Method | Description |
|:---|:---|:---|
| `/api/health` | `GET` | Daemon health and version status |
| `/api/metrics/overview` | `GET` | Summary counters: runs, events, spend, thrashing count |
| `/api/metrics/thrashing` | `GET` | Nodes mutated $\ge 3\times$ in 24h (`?threshold=3&window_days=7`) |
| `/api/metrics/models` | `GET` | Model survival rate, cost per survived node, total spend |
| `/api/metrics/recent` | `GET` | Chronological stream of AST code churn events (`?limit=50`) |
| `/api/file/health` | `GET` | File health score and fragility (`?path=src/auth.go`) |
| `/api/atlas` | `GET` | Code Atlas graph nodes and edges for React Flow |
| `/api/models/pricing` | `GET` | Multi-provider model pricing and context window catalog |
| `/api/models/sync` | `POST` | Force refresh pricing catalog from models.dev |
| `/api/profiler/overview` | `GET` | Profiler spans, latency percentiles (P50/P90/P99), error rates |
| `/api/profiler/hotspots` | `GET` | Top 10 runtime latency bottlenecks |
| `/api/gateway/stats` | `GET` | Wire proxy metrics: tokens, reasoning tokens, cache savings |
| `/api/settings` | `GET` / `PUT` | Read or update webhook URLs, retention days, and guardrails |
| `/api/settings/vacuum` | `POST` | Execute SQLite VACUUM optimization |
| `/api/settings/prune` | `POST` | Delete records older than retention threshold |

---

## 2. WebSocket Real-Time Feed (`ws://localhost:4318/api/ws`)

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
    "run_id": "sess-402",
    "task_id": "TASK-12",
    "agent_name": "Claude Code",
    "model_name": "claude-3-7-sonnet",
    "prompt_tokens": 12000,
    "completion_tokens": 850,
    "cost_usd": 0.048,
    "intent": "Refactor auth middleware"
  }
}
```

---

## 3. Model Context Protocol (MCP) Server

Executed as `wrongtrace mcp` over `stdin`/`stdout` (JSON-RPC 2.0).

### Available Tools:
1. **`report_telemetry`**: `{ model, provider, task_id, intent, tokens_used, cost }`
2. **`check_guardrail`**: `{ file_path }` → `{ allowed: bool, health_score: int, is_fragile: bool, recommendation: string }`
3. **`get_file_health_score`**: `{ file_path }` → `{ health_score: int, is_fragile: bool, recent_thrashing_count: int }`
4. **`lock_file`**: `{ file_path, reason }`
5. **`unlock_file`**: `{ file_path }`

---

## 4. Local IPC Protocol (Named Pipe / Unix Socket)

* **Windows:** `\\.\pipe\wrongtrace`
* **Linux/macOS:** `~/.wrongtrace/wrongtrace.sock`

### Methods:
* `telemetry/report_run`
* `telemetry/file_health`
