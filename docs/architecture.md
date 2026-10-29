# WrongTrace Architecture & Engineering Deep-Dive

WrongTrace is designed as a **zero-dependency, high-throughput, real-time AI Observability engine** for software codebases monitored during AI agent development.

---

## 1. High-Level System Architecture

```mermaid
flowchart TD
    subgraph Sources ["AI Agent & Ingestion Sources"]
        A1["Coding Agents (Claude Code, Cursor, Antigravity, Cline, Devin, MiniMax, Kimi, Pi)"]
        A2["AI Gateway Proxy (:3444/proxy OpenAI/Anthropic/Gemini)"]
        A3["OpenTelemetry OTLP & Profiler Traces (:3444/v1/traces)"]
        A4["File System Events (fsnotify Watcher)"]
    end

    subgraph CoreEngine ["WrongTrace Core Engine"]
        E1["Session Watcher & Ingestion Parser"]
        E2["Time-Windowed Event Correlator"]
        E3["Tree-sitter Semantic AST Diff Engine"]
        E4["File Health & Guardrails Subsystem"]
        E5["Model Pricing Registry (models.dev live sync)"]
    end

    subgraph Storage ["Embedded High-Performance Storage"]
        S1["SQLite with Write-Ahead Logging (WAL)"]
        S2["In-Memory Ring Buffers & Caches"]
    end

    subgraph Interfaces ["Egress & Observability Interfaces"]
        I1["Embedded React 19 + Recharts Dashboard (:3444)"]
        I2["WebSocket Live Event Broadcaster (/api/ws)"]
        I3["Model Context Protocol (MCP) Stdio Server"]
        I4["IPC Named Pipe / Unix Socket (\\\\.\\pipe\\wrongtrace)"]
        I5["Slack / Discord Webhook Dispatcher"]
    end

    Sources --> CoreEngine
    CoreEngine --> Storage
    Storage --> Interfaces
```

---

## 2. Core Subsystems

### A. Semantic AST Diff Engine (`internal/ast/`)
* **Tree-sitter Native Grammar Parsers**: Go, TypeScript, JavaScript and Python are parsed with native Tree-sitter grammars. The remaining supported languages — Rust, C/C++, Java, C#, PHP, Ruby, Kotlin and Dart — have no compiled grammar in this repo and go through the documented `parseGenericSource` heuristic fallback, so their declarations are recognised from line structure rather than a full parse tree.
* **Symbol Identity**: Generates unique node signatures of the form `kind:<basename>::<name>` (e.g., `func:auth.go::ValidateToken`, `method:server.go::*Server.Handle`, `class:ui.tsx::Dashboard`).
* **Normalized AST Hashing**: Strips non-semantic whitespace and comment volatility to detect true structural mutations.
* **Diff Classification**: Categorizes mutations into `ADDED`, `MODIFIED`, and `DELETED` nodes.

### B. Thrashing & Fragility Detection Algorithm (`internal/core/guardrails.go`)
* **Definition of Thrashing**: When the same AST node is mutated $\ge 3$ times within a sliding 24-hour window.
* **Health Score Formula**:
  $$\text{HealthScore} = \max\left(0, 100 - (\text{edits in the last 24h} \times 8) - (5 \text{ if distinct signatures} \ge 3)\right)$$
* **Fragile Threshold**: `RecentThrashingCount >= 5` marks `is_fragile = true`. A low score alone does not: 4 edits in 24h scores 68 and is *not* fragile. A lock does not affect the score — it is a separate guardrail that blocks modification outright.

### C. True Token ROI & Code Longevity (`internal/db/queries.go`)
* **Survival Window**: 14-day maturity window.
* **Survival Rate**: the share of a model's nodes still alive, i.e. $\frac{\text{Active Nodes}}{\text{Total Nodes Introduced by Model}} \times 100\%$. The 14-day maturity window below governs the ROI survivor count, not this ratio.
* **Cost Per Survived Node**:
  $$\text{CostPerSurvivedNode} = \frac{\text{Total Model Dollar Spend}}{\text{Surviving AST Nodes}}$$

### D. Multi-Channel Interceptor Layer
1. **Model Context Protocol (MCP)**: Implements JSON-RPC 2.0 over stdio (`protocolVersion: 2024-11-05`).
2. **IPC Named Pipe**: Windows `\\.\pipe\wrongtrace` and Unix `/tmp/wrongtrace.sock`.
3. **AI Gateway Proxy**: Intercepts streaming SSE chunks, calculates prompt caching discounts, and extracts hidden `<think>` reasoning tokens.
4. **Log Ingestor**: Auto-scans 20+ agent directories continuously.

---

### 4. Transparent AI Gateway Proxy (`internal/proxy/`)
* **Live Ingestion**: Intercepts OpenAI, Anthropic, Gemini, DeepSeek, Groq, Mistral, and custom endpoints.
* **Opt-in Scoped Response Caching (`internal/proxy/cache.go`)**: Requests must send `X-WrongTrace-Cache: allow`. Deterministic SHA-256 keys include a one-way credential/project/agent/session scope so cached completions cannot cross those boundaries.
* **Explicit Policy Mode**: Normal proxying is byte-preserving. `X-WrongTrace-Policy: enforce` enables all mutating/interrupting behavior, including secret redaction, quota blocking, OpenAI usage-option injection, and missing terminal-marker repair.
* **Real-time Secret & Leak Scanner (`internal/proxy/scanner.go`)**: In policy mode, masks AWS keys, GitHub tokens, database connection credentials, and `.env` private keys before outgoing payloads reach cloud LLMs.
* **Token Budget & Quota Guardrail (`internal/proxy/quota.go`)**: In policy mode, enforces daily spending limits per project/agent with `429 Quota Exceeded` protection.
* **Deep Wire Telemetry**: Extracts token usage, thinking blocks (`<think>`), tool calls, and prompt cache hit rates (`cache_read_input_tokens`).

---

## 3. Database Schema (`internal/db/`)

* **`agent_runs`**: Records run metadata, intent, model, provider, tokens, and USD cost.
* **`code_node_events`**: Granular AST diff records tied to file paths, function signatures, and diff text.
* **`runtime_traces`**: OpenTelemetry spans, durations, error rates, and command traces (percentiles are computed on read, not stored).
* **`file_read_events`**: Per-file read telemetry captured from agent transcripts, the watcher, and the gateway.

Guardrail locks are **not** a table: they live in the engine's in-memory map with their own TTL sweep.
