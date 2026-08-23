-- WrongTrace analytical schema.
-- Two normalized tables: agent_runs (one row per agent invocation) and
-- code_node_events (one row per AST node transition). Everything else is
-- computed via queries in queries.go to keep writes append-only and fast.

CREATE TABLE IF NOT EXISTS agent_runs (
    run_id           VARCHAR PRIMARY KEY,
    task_id          VARCHAR NOT NULL,
    agent_name       VARCHAR NOT NULL,
    model_name       VARCHAR NOT NULL,
    provider         VARCHAR NOT NULL,
    prompt_tokens    BIGINT  DEFAULT 0,
    completion_tokens BIGINT DEFAULT 0,
    cost_usd         DOUBLE  DEFAULT 0.0,
    intent           TEXT,
    created_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS code_node_events (
    event_id         VARCHAR PRIMARY KEY,
    run_id           VARCHAR,
    repo_name        VARCHAR NOT NULL,
    file_path        VARCHAR NOT NULL,
    node_signature   VARCHAR NOT NULL,
    node_type        VARCHAR NOT NULL,
    action           VARCHAR NOT NULL,
    ast_content_hash VARCHAR(64),
    lines_of_code    INTEGER,
    start_line       INTEGER DEFAULT 0,
    end_line         INTEGER DEFAULT 0,
    diff_snippet     TEXT DEFAULT '',
    added_lines      INTEGER DEFAULT 0,
    deleted_lines    INTEGER DEFAULT 0,
    event_time       TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_node_sig   ON code_node_events(file_path, node_signature);
CREATE INDEX IF NOT EXISTS idx_node_time  ON code_node_events(event_time);
CREATE INDEX IF NOT EXISTS idx_node_run   ON code_node_events(run_id);
CREATE INDEX IF NOT EXISTS idx_runs_model ON agent_runs(model_name);

CREATE TABLE IF NOT EXISTS runtime_traces (
    trace_id         VARCHAR PRIMARY KEY,
    run_id           VARCHAR,
    service_name     VARCHAR NOT NULL,
    node_signature   VARCHAR DEFAULT '',
    file_path        VARCHAR DEFAULT '',
    duration_ms      DOUBLE DEFAULT 0.0,
    cpu_usage_pct    DOUBLE DEFAULT 0.0,
    memory_bytes     BIGINT DEFAULT 0,
    status_code      INTEGER DEFAULT 200,
    error_msg        TEXT DEFAULT '',
    profiler_type    VARCHAR NOT NULL, -- 'otlp', 'pprof', 'test_runner', 'custom'
    metadata_json    TEXT DEFAULT '{}',
    timestamp        TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_trace_sig   ON runtime_traces(file_path, node_signature);
CREATE INDEX IF NOT EXISTS idx_trace_time  ON runtime_traces(timestamp);
CREATE INDEX IF NOT EXISTS idx_trace_type  ON runtime_traces(profiler_type);

CREATE TABLE IF NOT EXISTS file_read_events (
    read_id          VARCHAR PRIMARY KEY,
    run_id           VARCHAR,
    session_id       VARCHAR,
    repo_name        VARCHAR NOT NULL,
    file_path        VARCHAR NOT NULL,
    agent_name       VARCHAR NOT NULL,
    model_name       VARCHAR NOT NULL,
    provider         VARCHAR NOT NULL,
    tool_name        VARCHAR NOT NULL,
    start_line       INTEGER DEFAULT 1,
    end_line         INTEGER DEFAULT 0,
    lines_read_count INTEGER DEFAULT 0,
    prompt_tokens    BIGINT DEFAULT 0,
    cached_tokens    BIGINT DEFAULT 0,
    cost_usd         DOUBLE DEFAULT 0.0,
    intent           TEXT DEFAULT '',
    read_time        TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_read_file_model ON file_read_events(file_path, model_name);
CREATE INDEX IF NOT EXISTS idx_read_time       ON file_read_events(read_time);
CREATE INDEX IF NOT EXISTS idx_read_repo       ON file_read_events(repo_name);
CREATE INDEX IF NOT EXISTS idx_read_lines      ON file_read_events(file_path, start_line, end_line);

