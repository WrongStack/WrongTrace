# Integrating WrongTrace with your agents and projects

WrongTrace observes a repository and correlates AST-level code churn with the
AI agent runs that produced it. Connecting a project takes one daemon plus
one of three telemetry paths. Everything below matches the implementation
verbatim — tool names, methods, defaults, and endpoints.

- [Option A — MCP (recommended)](#option-a--mcp-recommended)
- [Option B — IPC socket / named pipe](#option-b--ipc-socket--named-pipe)
- [Option C — REST API](#option-c--rest-api)
- [Daemon setup](#1-start-the-daemon-in-your-project)
- [Claude Code](#claude-code) · [Cursor](#cursor) · [Windsurf / Cline](#windsurf--cline) · [Any MCP client](#any-mcp-client)

## The mental model

```
your agent ──(telemetry: "run X cost $0.14, model M")──▶ wrongtrace daemon
your agent ──(edits files)──▶ fsnotify ──▶ Tree-sitter AST diff
                                                        │
        churn events + agent runs ──correlated──▶ SQLite ──▶ dashboard
```

Correlation is **time-windowed**: the most recently reported active run is
credited with subsequent AST events in watched files. So the flow per task is
*report the run before you start editing*, then edit normally.

---

## Option A — MCP (recommended)

MCP is the best fit for agent runtimes (Claude Code, Cursor, Windsurf,
Cline) because the agent can both **report** what it's doing and **query**
file fragility before touching code.

Run the daemon once per machine; register the MCP server per client:

```jsonc
// Claude Code / Cursor / Windsurf / Cline — mcpServers entry
{
  "mcpServers": {
    "wrongtrace": {
      "command": "/usr/local/bin/wrongtrace",   // or the downloaded release binary
      "args": ["mcp"]
    }
  }
}
```

`wrongtrace mcp` speaks MCP over stdio and needs **no daemon running** — it
writes directly to the same SQLite database (`~/.wrongtrace/wrongtrace.db`,
override with `WRONGTRACE_HOME` or `--db`). You still run the daemon for
watching + the dashboard; the MCP subcommand just shares its database.

### Tools it exposes

**`report_telemetry`** — record a run before editing:

| Field | Type | Required |
|---|---|---|
| `model` | string | ✅ |
| `provider` | string | ✅ |
| `task_id` | string | ✅ |
| `intent` | string | ✅ |
| `tokens_used` | number | — |
| `cost` | number | — |

**`get_file_health_score`** — check a file before touching it:

| Field | Type | Required |
|---|---|---|
| `file_path` | string | ✅ |

Returns `{ health_score: 0-100, is_fragile: bool, recent_thrashing_count, warning }`
so the agent can avoid files currently being thrashed.

### Example: raw JSON-RPC over stdio

```bash
echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"demo","version":"1"}}}
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"report_telemetry","arguments":{"model":"claude-3-7-sonnet","provider":"anthropic","task_id":"T-402","intent":"Refactor auth middleware","tokens_used":42000,"cost":0.144}}}' | wrongtrace mcp
```

---

## Option B — IPC socket / named pipe

For agents and hooks that already speak JSON-RPC 2.0 over a socket (IDE
plugins, git hooks, CI scripts). The daemon must be running.

Default paths (override with `--socket` or `WRONGTRACE_SOCKET`):

| Platform | Path |
|---|---|
| Linux / macOS | `~/.wrongtrace/wrongtrace.sock` |
| Windows | `\\.\pipe\wrongtrace` |

### Methods

**`telemetry/report_run`** — richer than the MCP variant; run_id is yours:

```jsonc
{
  "jsonrpc": "2.0", "id": 1,
  "method": "telemetry/report_run",
  "params": {
    "run_id": "c62a8b9f-4351-41b2-8ec2-73a7d431d1a1",
    "task_id": "TASK-402",
    "agent_name": "Claude-Code",
    "model_name": "claude-3-7-sonnet",
    "provider": "anthropic",
    "prompt_tokens": 42000,
    "completion_tokens": 1200,
    "cost_usd": 0.144,
    "intent": "Refactor authentication middlewares to support JWT rotation"
  }
}
```

**`telemetry/file_health`** — `{"file_path": "src/auth.go"}` → health info.

### POSIX example

```bash
echo '{"jsonrpc":"2.0","method":"telemetry/report_run","params":{"run_id":"r1","task_id":"T-1","agent_name":"Claude-Code","model_name":"claude-3-7-sonnet","provider":"anthropic","prompt_tokens":42000,"completion_tokens":1200,"cost_usd":0.144,"intent":"Refactor auth"},"id":1}' | nc -U ~/.wrongtrace/wrongtrace.sock
```

On Windows, any named-pipe client works (PowerShell, Python `open` on the
pipe path, winio in Go).

---

## Option C — REST API

For dashboards, scripts, or anything that prefers HTTP. Read-only today;
served by the daemon (default `http://localhost:4318`).

| Endpoint | Returns |
|---|---|
| `GET /api/health` | daemon status |
| `GET /api/metrics/overview` | totals: runs, events, spend |
| `GET /api/metrics/thrashing` | nodes edited ≥3× in 24h |
| `GET /api/metrics/models` | survival rate + cost per survived node per model |
| `GET /api/metrics/recent` | recent AST events |
| `GET /api/file/health?path=src/auth.go` | file health score |
| `GET /api/ws` | WebSocket: live `code_event` / `run_reported` frames |

---

## 1. Start the daemon in your project

```bash
# from the release binary (download from GitHub Releases)
./wrongtrace start --watch /path/to/your/project --repo my-app --port 4318
```

Then open <http://localhost:4318> — the dashboard shows live churn as any
agent (or human) edits the watched tree. `--repo` is just a label recorded
on events; `--watch` supports Go, TypeScript/JavaScript, and Python files.

Run one daemon per repository (each gets its own `--port` and `--db` if you
run several side by side). All flags:

| Flag | Default | Description |
|---|---|---|
| `--watch, -w` | `.` | Root directory to observe |
| `--port, -p` | `4318` | HTTP port for the dashboard + API |
| `--db` | `~/.wrongtrace/wrongtrace.db` | SQLite database file |
| `--socket` | platform default (above) | IPC socket / pipe path |
| `--repo` | basename of cwd | Repository label on events |

## Claude Code

Add to `.mcp.json` (project) or `~/.claude.json` (global):

```jsonc
{ "mcpServers": { "wrongtrace": { "command": "wrongtrace", "args": ["mcp"] } } }
```

## Cursor

Settings → MCP → Add server → stdio:

- Command: `wrongtrace`
- Arguments: `mcp`

(Or edit `~/.cursor/mcp.json` with the same JSON as Claude Code.)

## Windsurf / Cline

Windsurf: edit `~/.codeium/windsurf/mcp_config.json`; Cline: VS Code settings →
Cline → MCP Servers → Configure. Both use the same shape:

```jsonc
{ "mcpServers": { "wrongtrace": { "command": "wrongtrace", "args": ["mcp"], "disabled": false } } }
```

## Any MCP client

`wrongtrace mcp` is a standard stdio MCP server (protocol `2024-11-05`) with
two tools: `report_telemetry`, `get_file_health_score`. Point any
MCP-capable client at that command pair.

---

## Recommended workflow per task

1. **Report the run** (`report_telemetry` / `telemetry/report_run`) *before*
   editing — correlation is time-windowed, last-reported run wins.
2. **Check fragility** (`get_file_health_score`) on files you're about to
   touch — a low score means the file is being thrashed right now.
3. **Edit normally.** The watcher debounces, parses, and diffs; events appear
   on the dashboard within a couple of seconds.
4. Review churn/thrashing/ROI on the dashboard after the task.

## Troubleshooting

- **No events on the dashboard** — the watcher ignores `node_modules`,
  `.git`, `vendor`, `dist`, `build`, `target` (and their nested content);
  confirm the file is a supported language and lives under `--watch`.
- **`wrongtrace: not found`** — download a release binary from
  <https://github.com/ersinkoc/wrongtrace/releases> (linux-amd64,
  darwin-amd64/arm64, windows-amd64) or `make build` from source.
- **MCP runs but telemetry never shows** — the MCP subcommand and the daemon
  must resolve to the same database; check `WRONGTRACE_HOME` / `--db` are
  identical for both.
- **Windows IPC errors** — the pipe must be `\\.\pipe\...`; set
  `WRONGTRACE_SOCKET` if you need a different pipe name.
