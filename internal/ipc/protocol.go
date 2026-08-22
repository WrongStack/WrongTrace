// Package ipc implements the agent-facing JSON-RPC channel used by WrongTrace.
// On POSIX systems it exposes a Unix Domain Socket at /tmp/wrongtrace.sock; on
// Windows it falls back to a Named Pipe. Agents send a stream of telemetry
// reports; the daemon writes them to the database and broadcasts them to the
// dashboard via the WebSocket hub.
package ipc

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

// TelemetryReport is the typed payload for "telemetry/report_run". We do not
// import the db package here to keep IPC serialization independent; the
// engine maps these to db.RunRecord.
type TelemetryReport struct {
	RunID            string  `json:"run_id"`
	TaskID           string  `json:"task_id"`
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
}
