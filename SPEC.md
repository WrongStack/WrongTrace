```markdown
# WRONGTRACE - AI-NATIVE CODE CHURN & AGENT OBSERVABILITY DAEMON
## Complete System Specification & Implementation Blueprint

---

## 1. PROJECT OVERVIEW & GOAL

**WrongTrace** is a self-contained, single-binary telemetry daemon and analytical dashboard built in **Go** with an embedded **React (Vite + Tailwind + Lucide/Recharts)** frontend.

It acts as an autonomous runtime observer for AI-driven development (Claude Code, Cursor, Devin, Windsurf, Cline), answering key governance questions:
1. **Trace Semantic Churn & Code Rot:** Tracks the real-time lifecycle of functions and classes using Tree-sitter AST diffing.
2. **Detect Agent Thrashing & Regressions:** Flags when agents repeatedly rewrite, break, or delete the same AST nodes within 24 hours.
3. **Trace True Token ROI ($ per Survived Node):** Analyzes total prompt and completion spend against code nodes surviving $>14\text{ days}$ versus wasted/discarded code.
4. **Bi-directional Agent Telemetry & Guardrails:** Exposes IPC (Unix Domain Socket / Windows Named Pipe) and MCP (Model Context Protocol) interfaces so agents can report intent/token metrics and query file fragility warnings before touching code.

The compiled React frontend is embedded directly into the Go binary via `//go:embed`, producing a single deployable executable (`wrongtrace`).

---

## 2. SYSTEM ARCHITECTURE


```

```
                             [ Autonomous Coding Agent ]
                                 │ (MCP Tools / IPC)
                                 ▼
                        /tmp/wrongtrace.sock
                                 │

```

┌────────────────────────────────────┼────────────────────────────────────────┐
│ GO DAEMON (wrongtrace)             │                                        │
│                                    ▼                                        │
│  ┌──────────────────────┐  ┌───────────────┐  ┌──────────────────────────┐  │
│  │ fsnotify File Watcher│  │ IPC Listener  │  │ MCP Stdio Server (Subcmd)│  │
│  └──────────┬───────────┘  └───────┬───────┘  └─────────────┬────────────┘  │
│             │                      │                        │               │
│             ▼                      ▼                        ▼               │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │ Event Correlation Engine (Sliding Ingestion Window & Active Run State) │  │
│  └─────────────────────────────────┬─────────────────────────────────────┘  │
│                                    ▼                                        │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │ Tree-sitter AST Semantic Diff Engine (Go, TS/JS, Python)              │  │
│  │  - Extracts Signatures: func:pkg.name(args)                           │  │
│  │  - Normalized SHA256 AST Body Hashing                                 │  │
│  └─────────────────────────────────┬─────────────────────────────────────┘  │
│                                    ▼                                        │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │ Embedded Storage Layer (DuckDB / SQLite with Analytical Schemas)      │  │
│  │  - Tables: `agent_runs`, `code_node_events`                           │  │
│  └─────────────────────────────────┬─────────────────────────────────────┘  │
│                                    ▼                                        │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │ Embedded Web Server (go-chi HTTP + WebSockets + go:embed dist/)       │  │
│  │  - REST Endpoints: /api/stats, /api/thrashing, /api/models, /api/ws   │  │
│  └───────────────────────────────────────────────────────────────────────┘  │
└────────────────────────────────────┬────────────────────────────────────────┘
▼
http://localhost:4318 (React UI)

```

---

## 3. TECH STACK REQUIREMENTS

### Backend (Go 1.22+)
* **Router & HTTP:** `github.com/go-chi/chi/v5`, `github.com/go-chi/cors`, `github.com/gorilla/websocket`
* **File Watcher:** `github.com/fsnotify/fsnotify`
* **Embedded DB:** `github.com/marcboeker/go-duckdb` (or `modernc.org/sqlite` / `mattn/go-sqlite3`)
* **AST Parsing:** `github.com/smacker/go-tree-sitter` (supporting Go, TypeScript/JavaScript, Python)
* **CLI Engine:** `github.com/spf13/cobra`

### Frontend (React 18 / Vite / TypeScript)
* **Build Tool:** Vite
* **Styling:** Tailwind CSS + Lucide React (Icons)
* **Charts & Visuals:** Recharts or Tremor
* **State & Realtime:** Native WebSocket hooks + TanStack Query

---

## 4. DIRECTORY & FILE LAYOUT


```

wrongtrace/
├── cmd/
│   └── wrongtrace/
│       └── main.go                 # Cobra root: 'start', 'mcp', 'status' commands
├── internal/
│   ├── ast/
│   │   ├── parser.go               # Tree-sitter multi-language parser
│   │   ├── diff.go                 # Compares AST trees, emits ADD/MOD/DEL events
│   │   └── supported.go            # File extension to language mapping
│   ├── core/
│   │   ├── engine.go               # Coordinates FS events, Agent telemetry, and DB writes
│   │   ├── correlation.go          # Matches anonymous file edits to active Agent Run IDs
│   │   └── metrics.go              # Churn, Thrashing, Survival rate calculations
│   ├── db/
│   │   ├── schema.sql              # Table definitions
│   │   ├── db.go                   # DuckDB / SQLite connection management
│   │   └── queries.go              # Analytical aggregation queries
│   ├── ipc/
│   │   ├── socket.go               # Unix Domain Socket & Windows Named Pipe server
│   │   └── protocol.go             # JSON-RPC event models
│   ├── mcp/
│   │   └── server.go               # Stdio MCP Server handling tool execution
│   ├── server/
│   │   ├── server.go               # Chi router setup, static assets handler
│   │   ├── handlers.go             # REST API routes
│   │   └── ws.go                   # WebSocket Hub for realtime UI streaming
│   └── watcher/
│       └── watcher.go              # fsnotify wrapper with debouncing & ignore rules
├── web/                            # React Frontend Source
│   ├── index.html
│   ├── package.json
│   ├── tsconfig.json
│   ├── vite.config.ts
│   ├── tailwind.config.js
│   └── src/
│       ├── main.tsx
│       ├── App.tsx
│       ├── types/
│       │   └── index.ts
│       ├── hooks/
│       │   ├── useWebSocket.ts
│       │   └── useMetrics.ts
│       ├── components/
│       │   ├── Navbar.tsx
│       │   ├── MetricsOverview.tsx
│       │   ├── ThrashingHeatmap.tsx
│       │   ├── ModelLeaderboard.tsx
│       │   ├── LiveEventFeed.tsx
│       │   └── ROIAnalysis.tsx
│       └── pages/
│           └── Dashboard.tsx
├── embed.go                        # //go:embed web/dist/* wrapper
├── Makefile                        # Build targets for frontend + backend binary
├── go.mod
└── go.sum

```

---

## 5. DATABASE SCHEMA & ANALYTICAL QUERIES

### 5.1 SQL DDL (`internal/db/schema.sql`)
```sql
CREATE TABLE IF NOT EXISTS agent_runs (
    run_id VARCHAR PRIMARY KEY,
    task_id VARCHAR NOT NULL,
    agent_name VARCHAR NOT NULL,       -- e.g., 'Claude-Code', 'Cursor-Agent', 'Devin'
    model_name VARCHAR NOT NULL,       -- e.g., 'claude-3-7-sonnet', 'gpt-5'
    provider VARCHAR NOT NULL,         -- e.g., 'anthropic', 'openai'
    prompt_tokens BIGINT DEFAULT 0,
    completion_tokens BIGINT DEFAULT 0,
    cost_usd DOUBLE DEFAULT 0.0,
    intent TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS code_node_events (
    event_id VARCHAR PRIMARY KEY,
    run_id VARCHAR,                    -- Correlated run ID
    repo_name VARCHAR NOT NULL,
    file_path VARCHAR NOT NULL,
    node_signature VARCHAR NOT NULL,   -- e.g., 'func:auth.ValidateToken(string)'
    node_type VARCHAR NOT NULL,        -- 'function', 'class', 'method', 'struct'
    action VARCHAR NOT NULL,           -- 'ADDED', 'MODIFIED', 'DELETED'
    ast_content_hash VARCHAR(64),      -- SHA256 of the node's normalized AST body
    lines_of_code INTEGER,
    event_time TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_node_sig ON code_node_events(file_path, node_signature);
CREATE INDEX IF NOT EXISTS idx_node_time ON code_node_events(event_time);

```

### 5.2 Core Metrics SQL Queries (`internal/db/queries.go`)

#### 1. Thrashing Detection (Patinaj)

```sql
SELECT 
    file_path,
    node_signature,
    COUNT(*) AS edit_count,
    MIN(event_time) AS first_event,
    MAX(event_time) AS last_event,
    DATE_DIFF('hour', MIN(event_time), MAX(event_time)) AS window_hours
FROM code_node_events
WHERE event_time >= CURRENT_TIMESTAMP - INTERVAL 7 DAY
GROUP BY file_path, node_signature
HAVING edit_count >= 3 AND window_hours <= 24
ORDER BY edit_count DESC
LIMIT 25;

```

#### 2. Model Survival Rate & Longevity

```sql
WITH lifecycle AS (
    SELECT 
        e.node_signature,
        r.model_name,
        MIN(e.event_time) AS birth_time,
        MAX(CASE WHEN e.action = 'DELETED' THEN e.event_time ELSE NULL END) AS death_time
    FROM code_node_events e
    INNER JOIN agent_runs r ON e.run_id = r.run_id
    GROUP BY e.node_signature, r.model_name
)
SELECT 
    model_name,
    COUNT(*) AS total_nodes,
    COUNT(CASE WHEN death_time IS NULL THEN 1 END) AS active_nodes,
    ROUND(COUNT(CASE WHEN death_time IS NULL THEN 1 END) * 100.0 / NULLIF(COUNT(*), 0), 2) AS survival_rate_pct,
    ROUND(AVG(DATE_DIFF('day', birth_time, COALESCE(death_time, CURRENT_TIMESTAMP))), 1) AS avg_longevity_days
FROM lifecycle
GROUP BY model_name
ORDER BY survival_rate_pct DESC;

```

#### 3. True Token ROI ($ per Survived Function)

```sql
WITH survivals AS (
    SELECT 
        run_id,
        COUNT(DISTINCT node_signature) AS surviving_count
    FROM code_node_events
    WHERE action = 'ADDED' 
      AND event_time <= CURRENT_TIMESTAMP - INTERVAL 14 DAY
      AND node_signature NOT IN (
          SELECT node_signature FROM code_node_events WHERE action = 'DELETED'
      )
    GROUP BY run_id
)
SELECT 
    r.model_name,
    ROUND(SUM(r.cost_usd), 2) AS total_cost_usd,
    COALESCE(SUM(s.surviving_count), 0) AS total_survived_nodes,
    ROUND(SUM(r.cost_usd) / NULLIF(SUM(s.surviving_count), 0), 4) AS cost_per_surviving_node
FROM agent_runs r
LEFT JOIN survivals s ON r.run_id = s.run_id
GROUP BY r.model_name
ORDER BY cost_per_surviving_node ASC;

```

---

## 6. IPC PROTOCOL & MCP SPECIFICATION

### 6.1 Unix Domain Socket Protocol (`/tmp/wrongtrace.sock`)

Agents or IDE hooks connect and stream JSON messages:

```json
{
  "jsonrpc": "2.0",
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

### 6.2 Model Context Protocol (MCP) Server

When executed via `wrongtrace mcp`, exposes standard MCP JSON-RPC over `stdio` with two tools:

1. `report_telemetry`:
* Input: `{ model: string, provider: string, task_id: string, intent: string, tokens_used?: number, cost?: number }`
* Output: `Telemetry recorded successfully.`


2. `get_file_health_score`:
* Input: `{ file_path: string }`
* Output: `{ file_path: string, health_score: 0-100, is_fragile: boolean, recent_thrashing_count: number, warning: string }`
* Purpose: Prevents agents from blindly modifying files currently undergoing rapid churn.



---

## 7. TREE-SITTER AST PARSER IMPLEMENTATION LOGIC

File changes detected by `fsnotify` trigger AST processing in `internal/ast/diff.go`:

1. **Snapshot Cache:** Maintain previous AST states in an in-memory LRU cache (`map[string]*FileSnapshot`).
2. **Node Traversal:**
* **Go:** Match `function_declaration`, `method_declaration`, `type_spec`.
* **TypeScript/JS:** Match `function_declaration`, `method_definition`, `arrow_function`, `class_declaration`.
* **Python:** Match `function_definition`, `class_definition`.


3. **Signature Generation:** Format normalized names, e.g., `func:auth.go::ValidateToken`.
4. **Hashing:** Extract byte slice of the node body, strip whitespace/comments, compute `SHA256(body)`.
5. **Diffing:**
* Node exists in new, not in old $\rightarrow$ `ADDED`
* Node exists in both, hash changed $\rightarrow$ `MODIFIED`
* Node in old, missing in new $\rightarrow$ `DELETED`



---

## 8. EMBEDDED WEB SERVER & REACT DASHBOARD

### 8.1 Go Embedding & Routing (`embed.go`, `internal/server/server.go`)

```go
package main

import (
    "embed"
    "io/fs"
    "net/http"
    "[github.com/go-chi/chi/v5](https://github.com/go-chi/chi/v5)"
)

//go:embed web/dist/*
var embeddedWebFS embed.FS

func SetupRoutes(r chi.Router) {
    // API Routes
    r.Route("/api", func(r chi.Router) {
        r.Get("/health", HandleHealth)
        r.Get("/metrics/overview", HandleMetricsOverview)
        r.Get("/metrics/thrashing", HandleThrashingList)
        r.Get("/metrics/models", HandleModelComparison)
        r.Get("/ws", HandleWebSocket)
    })

    // Embedded React Static Files with SPA Fallback
    distFS, _ := fs.Sub(embeddedWebFS, "web/dist")
    fileServer := http.FileServer(http.FS(distFS))
    r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
        path := r.URL.Path
        if _, err := distFS.Open(path[1:]); err != nil {
            r.URL.Path = "/"
        }
        fileServer.ServeHTTP(w, r)
    })
}

```

### 8.2 React Dashboard UI Layout & Widgets

1. **Header Bar:** Active Daemon status, Connected Agent Indicator, Socket status (`/tmp/wrongtrace.sock`), Repository name.
2. **Top Metrics Cards:**
* **Overall Churn Rate (7d):** % of code modified within 72h.
* **Active Thrashing Count:** Critical files undergoing rapid rewrites.
* **Total Agent Spend vs. Waste:** Spent USD vs. Churned/Discarded USD.
* **Top Performing Model:** Model with highest 14-day survival rate.


3. **Main Dashboard Grid:**
* **Left Panel:** Thrashing Heatmap & Fragile Nodes list (Sorted by edit count and churn window).
* **Right Panel:** Model Comparison Leaderboard (Survival % vs. Cost-per-survived-node).


4. **Bottom Panel:** Real-time Event Stream via WebSocket showing live AST modifications and Agent runs.

---

## 9. STEP-BY-STEP BUILD & EXECUTION GUIDE

### 9.1 Makefile

```makefile
.PHONY: all build build-ui build-go run clean

all: build

build-ui:
	cd web && npm install && npm run build

build-go:
	go build -o bin/wrongtrace ./cmd/wrongtrace/main.go

build: build-ui build-go

run: build
	./bin/wrongtrace start --watch ./src --port 4318

clean:
	rm -rf bin/ web/dist

```

### 9.2 Execution Commands

* **Start Observer Daemon & UI:**
`./bin/wrongtrace start --watch ./my-project --port 4318`
* **Run as MCP Server for Claude/Cursor:**
`./bin/wrongtrace mcp`
* **Check Status via CLI:**
`./bin/wrongtrace status`

---