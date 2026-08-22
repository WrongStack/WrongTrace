package ingest

import (
	"strings"
	"time"
)

// ToolCallEvent captures a file-modifying tool call executed by an AI agent.
type ToolCallEvent struct {
	SessionID        string    `json:"session_id"`
	AgentName        string    `json:"agent_name"`
	ModelName        string    `json:"model_name"`
	Provider         string    `json:"provider"`
	ToolName         string    `json:"tool_name"`
	TargetFile       string    `json:"target_file"`
	PromptTokens     int64     `json:"prompt_tokens"`
	CompletionTokens int64     `json:"completion_tokens"`
	CostUSD          float64   `json:"cost_usd"`
	Intent           string    `json:"intent"`
	OccurredAt       time.Time `json:"occurred_at"`
}

// KnownFileModifyingTools lists standard tool/function call names used by AI agents to mutate files.
var KnownFileModifyingTools = map[string]bool{
	"write_to_file":        true,
	"replace_file_content": true,
	"edit_file":            true,
	"str_replace_editor":   true,
	"create_file":          true,
	"delete_file":          true,
	"apply_diff":           true,
	"patch":                true,
	"write":                true,
	"save":                 true,
	"append":               true,
	"execute_command":      true,
	"run_command":          true,
	"bash":                 true,
}

// IsFileModifyingTool returns true if the tool name indicates a file system write operation.
func IsFileModifyingTool(name string) bool {
	norm := strings.ToLower(strings.TrimSpace(name))
	if KnownFileModifyingTools[norm] {
		return true
	}
	// Fuzzy matches
	return strings.Contains(norm, "write") ||
		strings.Contains(norm, "edit") ||
		strings.Contains(norm, "replace") ||
		strings.Contains(norm, "patch") ||
		strings.Contains(norm, "create")
}
