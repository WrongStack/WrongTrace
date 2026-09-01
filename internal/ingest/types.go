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
	// Suffix-shaped names a token-boundary stem match cannot see, because the
	// stem is not a token of its own. Kept explicit rather than widening the
	// stems, which is exactly what produced the false positives nameHasStem
	// prevents.
	"notebookedit": true,
	"multiedit":    true,
}

// toolNameStems are name fragments that identify tools absent from the explicit
// tables above. They are matched per name-token, never as a raw substring:
// strings.Contains on a 3-4 letter stem classified "locate_file" as a file read
// (lo|cat|e) and "credit_card_scan" as a file write (cr|edit), which injected
// bogus path->run attribution hints and phantom read telemetry.
var (
	modifyingStems = []string{"write", "edit", "replace", "patch", "create"}
	readingStems   = []string{"read", "view", "inspect", "cat"}
)

// derivationalSuffixes are the terminations that turn a stem into an inflected or
// agentive form of the SAME verb: "read" -> "reader", "write" -> "writers",
// "edit" -> "editing". Only these exact remainders count. Anything else is a
// different English word that merely starts with the stem -- "ready" ("y"),
// "readiness" ("iness") and "editable" ("able") describe states, not operations,
// and must not classify.
var derivationalSuffixes = map[string]bool{
	"s": true, "es": true, "r": true, "rs": true,
	"er": true, "ers": true, "or": true, "ors": true,
	"ing": true, "ed": true,
}

// derivationalPrefixes are the bound prefixes that preserve the stem's meaning:
// "write" -> "overwrite", "create" -> "recreate". Deliberately absent are "dis"
// and "des" (dispatch, despatch -- unrelated verbs), "mis" (misread) and "pre"
// (preview), all of which end in a stem by accident. "pre" is excluded on
// purpose: KnownFileReadingTools pins "preview_mode" as a non-match.
var derivationalPrefixes = map[string]bool{
	"over": true, "under": true, "out": true, "re": true,
}

// nameHasStem reports whether any token of the normalized tool name denotes one
// of the stems: exactly ("code_edit"), as a stem with a derivational suffix
// ("reader", "inspector"), or as a stem with a derivational prefix
// ("overwrite"). Length still gates the affix cases -- stems of 4+ letters take
// a suffix, of 5+ a prefix -- so short common fragments ("thread", "credit",
// "locate", "catalog") only count when they stand alone. The affix itself must be
// a known derivation, because a bare length test let ordinary words ending or
// beginning in a stem ("dispatch", "ready", "editable") classify as file I/O.
// Scanned in place: no allocation, since this runs per tool call while parsing
// transcripts that can hold tens of thousands of lines.
func nameHasStem(name string, stems []string) bool {
	for start := 0; start <= len(name); {
		end := start
		for end < len(name) && !isNameSeparator(name[end]) {
			end++
		}
		token := name[start:end]
		for _, stem := range stems {
			switch {
			case token == stem:
				return true
			case len(stem) >= 4 && strings.HasPrefix(token, stem) &&
				derivationalSuffixes[token[len(stem):]]:
				return true
			case len(stem) >= 5 && strings.HasSuffix(token, stem) &&
				derivationalPrefixes[token[:len(token)-len(stem)]]:
				return true
			}
		}
		start = end + 1
	}
	return false
}

func isNameSeparator(c byte) bool {
	switch c {
	case '_', '-', '.', '/', ':', ' ':
		return true
	}
	return false
}

// IsFileModifyingTool returns true if the tool name indicates a file system write operation.
func IsFileModifyingTool(name string) bool {
	norm := strings.ToLower(strings.TrimSpace(name))
	if KnownFileModifyingTools[norm] {
		return true
	}
	// Fuzzy matches, scoped to name tokens -- see nameHasStem.
	return nameHasStem(norm, modifyingStems)
}

// FileReadEvent captures a file read/inspection tool call executed by an AI agent.
type FileReadEvent struct {
	ReadID         string    `json:"read_id"`
	SessionID      string    `json:"session_id"`
	RunID          string    `json:"run_id"`
	RepoName       string    `json:"repo_name"`
	FilePath       string    `json:"file_path"`
	AgentName      string    `json:"agent_name"`
	ModelName      string    `json:"model_name"`
	Provider       string    `json:"provider"`
	ToolName       string    `json:"tool_name"`
	StartLine      int       `json:"start_line"`
	EndLine        int       `json:"end_line"`
	LinesReadCount int       `json:"lines_read_count"`
	PromptTokens   int64     `json:"prompt_tokens"`
	CachedTokens   int64     `json:"cached_tokens"`
	CostUSD        float64   `json:"cost_usd"`
	Intent         string    `json:"intent"`
	OccurredAt     time.Time `json:"occurred_at"`
}

// KnownFileReadingTools lists standard tool/function call names used by AI agents to read files.
var KnownFileReadingTools = map[string]bool{
	"view_file":         true,
	"read_file":         true,
	"read_file_range":   true,
	"get_file_contents": true,
	"read":              true,
	"view":              true,
	"cat":               true,
	"head":              true,
	"tail":              true,
	"show_file":         true,
	"open_file":         true,
	"inspect_file":      true,
	"load_file":         true,
	"get_file_tree":     true,
	"grep_search":       true,
	"find_by_name":      true,
	"notebookread":      true, // suffix-shaped; see KnownFileModifyingTools
}

// IsFileReadingTool returns true if the tool name indicates a file system read/inspection operation.
func IsFileReadingTool(name string) bool {
	norm := strings.ToLower(strings.TrimSpace(name))
	if KnownFileReadingTools[norm] {
		return true
	}
	// Avoid false positive on write tools
	if IsFileModifyingTool(norm) {
		return false
	}
	// Fuzzy matches, scoped to name tokens -- see nameHasStem.
	return nameHasStem(norm, readingStems)
}
