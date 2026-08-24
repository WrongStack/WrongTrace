# WrongTrace API Reference

WrongTrace exposes multiple protocol interfaces for bidirectional AI agent communication, live telemetry streaming, and observability dashboards.

---

## 1. REST API (`http://localhost:8000`)

| Method | Path | Description |
| :--- | :--- | :--- |
| `GET` | `/api/health` | Daemon status, watcher state, SQLite DB path, IPC socket. |
| `GET` | `/api/metrics/overview` | High-level metrics: files observed, thrash count, token costs. |
| `GET` | `/api/metrics/thrashing` | Files with highest thrashing / low survival rates. |
| `GET` | `/api/metrics/models` | Efficiency, total token spending, churn by LLM model. |
| `GET` | `/api/metrics/recent` | Recent edit / read telemetry events stream. |
| `GET` | `/api/atlas` | Semantic codebase map nodes, dependencies, and health. |
| `GET` | `/api/atlas/status` | Current progress of background code graph indexing. |
| `GET` | `/api/file/health?path=...` | Health score, fragility, and thrash history for a file. |
| `GET` | `/api/guardrail/check?path=...` | Check if a file is protected/locked against agent edits. |
| `POST` | `/api/guardrail/lock` | Lock a critical file (`{"path": "...", "reason": "..."}`). |
| `POST` | `/api/guardrail/unlock` | Unlock a previously locked file (`{"path": "..."}`). |
| `GET` | `/api/reads/recent` | Real-time file read events captured by agent tools. |
| `GET` | `/api/files/reads` | Most frequently read files and token costs. |
| `GET` | `/api/files/heatmap?path=...` | Line-by-line read frequency heatmap for a file. |
| `GET` | `/api/proxy/routes` | Active AI reverse proxy routes. |
| `POST` | `/api/proxy/routes` | Register or update upstream AI route. |
| `DELETE` | `/api/proxy/routes/:id` | Remove a proxy route. |
| `GET` | `/api/proxy/traffic` | Intercepted AI completions, tokens, latency, cost. |
| `GET` | `/api/projects` | Multi-project workspace profiles and status. |
| `POST` | `/api/projects` | Register a new project workspace. |
| `GET` | `/api/models/catalog` | Dynamic pricing and context window registry. |
| `POST` | `/api/profiler/ingest` | Universal profiler test runner / CLI execution trace. |
| `POST` | `/v1/traces` | Standard OpenTelemetry OTLP trace receiver. |

---

## 2. WebSocket Real-Time Feed (`ws://localhost:8000/api/ws`)

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
