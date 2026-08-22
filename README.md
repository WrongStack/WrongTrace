# WrongTrace

Self-contained, single-binary telemetry daemon for AI-driven development.
Watches your code with Tree-sitter, correlates edits with the agent runs
that produced them, and serves a live React dashboard — all from one Go
binary with the UI embedded via `//go:embed`.

> **Questions WrongTrace answers:**
> 1. *Trace semantic churn & code rot* — function/method/class lifecycle via AST diffing.
> 2. *Detect agent thrashing & regressions* — same node rewritten ≥3× in a 24h window.
> 3. *True token ROI* — `$ / survived_node` for every model after a 14-day window.
> 4. *Bidirectional agent telemetry* — Unix Domain Socket / Named Pipe + MCP for
>    Claude Code, Cursor, Devin, Windsurf, and Cline.

---

## Quickstart

```bash
make build          # build the React UI + Go binary
make run            # start daemon on :4318 watching the current directory
```

Open <http://localhost:4318> for the dashboard. In another shell:

```bash
# Talk to the daemon from any agent that speaks JSON-RPC 2.0:
echo '{"jsonrpc":"2.0","method":"telemetry/report_run","params":{"run_id":"r1","task_id":"T-1","agent_name":"Claude-Code","model_name":"claude-3-7-sonnet","provider":"anthropic","prompt_tokens":42000,"completion_tokens":1200,"cost_usd":0.144,"intent":"Refactor auth middleware"},"id":1}' | nc -U /tmp/wrongtrace.sock

# …or expose the daemon as an MCP server (Claude Code, Cursor, Windsurf):
make run-mcp
```

## CLI

| Command            | What it does                                                          |
|--------------------|-----------------------------------------------------------------------|
| `wrongtrace start` | Run the observer daemon + embedded dashboard                          |
| `wrongtrace mcp`   | Serve the Model Context Protocol over stdio                           |
| `wrongtrace status`| Print a short summary (DB path, totals, cost)                         |

`start` flags:

| Flag                | Default                       | Description                          |
|---------------------|-------------------------------|--------------------------------------|
| `--watch, -w`       | `.`                           | Root directory to observe            |
| `--port, -p`        | `4318`                        | HTTP port for the embedded dashboard |
| `--db`              | `~/.wrongtrace/wrongtrace.db` | SQLite database file                 |
| `--socket`          | `~/.wrongtrace/wrongtrace.sock` | Unix socket / named pipe path      |
| `--repo`            | basename of cwd               | Repository name recorded in events   |

## Architecture

```
[ Autonomous Coding Agent ]  ── MCP / JSON-RPC ──▶  /tmp/wrongtrace.sock
                                                          │
                                                          ▼
   fsnotify ───▶ Event Correlation ───▶ Tree-sitter AST Diff ───▶ SQLite
       │                                                          │
       ▼                                                          ▼
   Debounce / ignore                                  WebSocket hub ───▶ React UI
                                                                  (http://localhost:4318)
```

Source layout:

```
cmd/wrongtrace/main.go      # cobra CLI: start | mcp | status
internal/
  ast/                       # tree-sitter parser + semantic diff
  core/                      # engine, event correlation, metrics, WS hub
  db/                        # sqlite schema, queries, IDs
  embedsrc/                  # //go:embed web/dist
  ipc/                       # unix socket / windows named pipe
  mcp/                       # stdio MCP server
  server/                    # chi router, REST handlers, WebSocket
  watcher/                   # fsnotify with debouncing + ignore rules
web/                         # Vite + React + Tailwind + Recharts
```

## Development

```bash
make build-ui    # rebuild React dashboard only
make build-go    # rebuild Go binary only (no UI changes)
make test        # go test ./...
make fmt vet
make tidy        # go mod tidy
```

The dev server proxies `/api` and `/api/ws` to the daemon on `:4318`:

```bash
make run &          # terminal 1: daemon
cd web && npm run dev  # terminal 2: hot-reload dashboard on :5173
```

## Notes on the embedded dashboard

`internal/embedsrc/embedsrc.go` uses `//go:embed all:web/dist` to bundle the
compiled React assets into the binary. A tiny `web/dist/index.html`
placeholder ships in this repo so the binary builds before you have run
`make build-ui` at least once. After running `make build-ui`, the binary
will embed the real dashboard automatically.
