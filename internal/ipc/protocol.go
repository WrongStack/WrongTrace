// Package ipc implements the agent-facing JSON-RPC channel used by WrongTrace.
// On POSIX systems it exposes a Unix Domain Socket at /tmp/wrongtrace.sock; on
// Windows it falls back to a Named Pipe. Agents send a stream of telemetry
// reports; the daemon writes them to the database and broadcasts them to the
// dashboard via the WebSocket hub.
package ipc

import "time"

// Request is the JSON-RPC 2.0 envelope used by the IPC channel. It is
// deliberately minimal: every method accepts a free-form Params map and
// returns a Result on success or an Error object on failure.
type Request struct {
	JSONRPC string                 `json:"jsonrpc"`
	Method  string                 `json:"method"`
	Params  map[string]interface{} `json:"params,omitempty"`
	ID      interface{}            `json:"id,omitempty"`
}

// Response mirrors the JSON-RPC 2.0 reply envelope.
type Response struct {
	JSONRPC string      `json:"jsonrpc"`
	Result  interface{} `json:"result,omitempty"`
	Error   *RPCError   `json:"error,omitempty"`
	ID      interface{} `json:"id"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// LockInfo records guardrail lock metadata including ownership and expiry TTL.
type LockInfo struct {
	Path       string    `json:"path"`
	Reason     string    `json:"reason"`
	Owner      string    `json:"owner,omitempty"`
	OwnerRunID string    `json:"owner_run_id,omitempty"`
	LockedAt   time.Time `json:"locked_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// GuardrailResult indicates whether an agent should proceed editing a file.
type GuardrailResult struct {
	Allowed              bool       `json:"allowed"`
	HealthScore          int        `json:"health_score"`
	RecentThrashingCount int        `json:"recent_thrashing_count"`
	IsFragile            bool       `json:"is_fragile"`
	IsLocked             bool       `json:"is_locked"`
	LockReason           string     `json:"lock_reason,omitempty"`
	LockOwner            string     `json:"lock_owner,omitempty"`
	LockOwnerRunID       string     `json:"lock_owner_run_id,omitempty"`
	LockExpiresAt        *time.Time `json:"lock_expires_at,omitempty"`
	Recommendation       string     `json:"recommendation"`
	CheckedAt            time.Time  `json:"checked_at"`
}

// TelemetryReport is the typed payload for "telemetry/report_run". We do not
// import the db package here to keep IPC serialization independent; the
// engine maps these to db.RunRecord.
type TelemetryReport struct {
	RunID            string  `json:"run_id"`
	TaskID           string  `json:"task_id"`
	ProjectID        string  `json:"project_id,omitempty"`
	ProjectSlug      string  `json:"project_slug,omitempty"`
	AgentName        string  `json:"agent_name"`
	ModelName        string  `json:"model_name"`
	Provider         string  `json:"provider"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	Intent           string  `json:"intent"`
}

// FileHealthQuery is the typed payload for "telemetry/file_health".
type FileHealthQuery struct {
	FilePath string `json:"file_path"`
	Path     string `json:"path,omitempty"`
}

// FileReadReport is the typed payload for "telemetry/report_file_read" or "report_file_read".
type FileReadReport struct {
	FilePath       string  `json:"file_path"`
	Path           string  `json:"path,omitempty"`
	ModelName      string  `json:"model_name,omitempty"`
	Model          string  `json:"model,omitempty"`
	AgentName      string  `json:"agent_name,omitempty"`
	LineCount      int     `json:"line_count,omitempty"`
	TokensConsumed int64   `json:"tokens_consumed,omitempty"`
	PromptTokens   int64   `json:"prompt_tokens,omitempty"`
	CostUSD        float64 `json:"cost_usd,omitempty"`
	RunID          string  `json:"run_id,omitempty"`
	TaskID         string  `json:"task_id,omitempty"`
	RepoName       string  `json:"repo_name,omitempty"`
}

// LockFileRequest is the typed payload for "lock_file" or "guardrail/lock".
type LockFileRequest struct {
	FilePath   string `json:"file_path"`
	Path       string `json:"path,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Owner      string `json:"owner,omitempty"`
	OwnerRunID string `json:"owner_run_id,omitempty"`
	TTLMinutes int    `json:"ttl_minutes,omitempty"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
	TTL        int    `json:"ttl,omitempty"`
}

// GuardrailCheckRequest is the typed payload for "check_guardrail" or "guardrail/check".
type GuardrailCheckRequest struct {
	FilePath string `json:"file_path"`
	Path     string `json:"path,omitempty"`
}

// AtlasRequest is the typed payload for "atlas" or "get_atlas".
type AtlasRequest struct {
	Summary bool   `json:"summary,omitempty"`
	Repo    string `json:"repo,omitempty"`
	Filter  string `json:"filter,omitempty"`
}

// DiffHistoryRequest is the typed payload for "get_file_diff_history" or "diff_history".
type DiffHistoryRequest struct {
	FilePath string `json:"file_path"`
	Path     string `json:"path,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}
