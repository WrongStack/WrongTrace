package ingest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/wrongstack/wrongtrace/internal/models"
)

// ExtractTargetFile attempts to extract the destination file path from a tool call payload.
func ExtractTargetFile(args map[string]interface{}) string {
	keys := []string{
		"AbsolutePath", "absolute_path", "TargetFile", "target_file", "targetFile", "TargetContent",
		"path", "FilePath", "file_path", "filePath", "filename", "fileName", "file_name", "file", "target", "dest",
		"URI", "uri", "Url", "url", "relative_path", "relativePath", "rel_path",
		"SourceFile", "source_file", "SearchPath", "search_path", "DirectoryPath", "directory_path",
	}
	for _, k := range keys {
		if val, ok := args[k]; ok {
			if s, isStr := val.(string); isStr && s != "" {
				return s
			}
		}
	}
	return ""
}

// ExtractLineRange extracts start line and end line from tool arguments if present.
func ExtractLineRange(args map[string]interface{}) (int, int, int) {
	startLine := 1
	endLine := 0
	linesCount := 0

	startKeys := []string{"StartLine", "start_line", "startLine", "offset", "line_start", "lineStart", "from_line", "fromLine", "start"}
	for _, k := range startKeys {
		if val, ok := args[k]; ok {
			if n := extractInt(val); n > 0 {
				startLine = n
				break
			}
		}
	}

	endKeys := []string{"EndLine", "end_line", "endLine", "line_end", "lineEnd", "to_line", "toLine", "end"}
	for _, k := range endKeys {
		if val, ok := args[k]; ok {
			if n := extractInt(val); n > 0 {
				endLine = n
				break
			}
		}
	}

	countKeys := []string{"lines", "line_count", "count", "num_lines", "limit"}
	for _, k := range countKeys {
		if val, ok := args[k]; ok {
			if n := extractInt(val); n > 0 {
				linesCount = n
				break
			}
		}
	}

	if linesCount == 0 && endLine >= startLine {
		linesCount = endLine - startLine + 1
	}
	if endLine == 0 && linesCount > 0 {
		endLine = startLine + linesCount - 1
	}

	return startLine, endLine, linesCount
}

func extractInt(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case string:
		var parsed int
		if _, err := fmt.Sscanf(n, "%d", &parsed); err == nil {
			return parsed
		}
	}
	return 0
}

// ParseJSONLTranscript parses a JSONL file line-by-line, extracting file modifying tool calls.
func ParseJSONLTranscript(filePath string) ([]ToolCallEvent, error) {
	modEvents, _, err := ParseJSONLTranscriptFull(filePath)
	return modEvents, err
}

// ParseJSONLTranscriptFull parses a JSONL file line-by-line from start to finish.
func ParseJSONLTranscriptFull(filePath string) ([]ToolCallEvent, []FileReadEvent, error) {
	modEvents, readEvents, _, err := ParseJSONLTranscriptFromOffset(filePath, 0)
	return modEvents, readEvents, err
}

// ParseJSONLTranscriptFromOffset streams a JSONL file line-by-line starting from startOffset without loading the entire file into memory.
func ParseJSONLTranscriptFromOffset(filePath string, startOffset int64) ([]ToolCallEvent, []FileReadEvent, int64, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, nil, startOffset, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()

	if startOffset > 0 {
		if _, err := f.Seek(startOffset, io.SeekStart); err != nil {
			return nil, nil, startOffset, fmt.Errorf("seek transcript: %w", err)
		}
	}

	reader := bufio.NewReaderSize(f, 64*1024)
	var modEvents []ToolCallEvent
	var readEvents []FileReadEvent
	sessionID := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	agentName := detectAgentFromPath(filePath)
	currentModel := detectAgentDefaultModel(agentName)
	currentIntent := ""
	currentOffset := startOffset

	for {
		lineBytes, err := reader.ReadBytes('\n')
		readLen := int64(len(lineBytes))
		currentOffset += readLen

		trimmed := bytes.TrimSpace(lineBytes)
		if len(trimmed) > 0 {
			var row map[string]interface{}
			if uErr := json.Unmarshal(trimmed, &row); uErr == nil {
				// Dynamically extract model from deep json structures
				if extracted := extractModelFromRow(row); extracted != "" && !models.IsJunkModel(extracted) {
					currentModel = extracted
				}

				if intent, ok := row["content"].(string); ok && row["type"] == "USER_INPUT" {
					if len(intent) > 80 {
						currentIntent = intent[:80] + "…"
					} else {
						currentIntent = intent
					}
				}

				// Extract usage tokens if present
				var promptTokens, completionTokens int64
				if usage, ok := row["usage"].(map[string]interface{}); ok {
					promptTokens = extractInt64(usage, "input_tokens", "prompt_tokens", "promptTokenCount", "inputTokens")
					completionTokens = extractInt64(usage, "output_tokens", "completion_tokens", "candidatesTokenCount", "outputTokens")
				}

				// Extract tool calls
				if toolCalls, ok := row["tool_calls"].([]interface{}); ok {
					for _, tc := range toolCalls {
						tcMap, ok := tc.(map[string]interface{})
						if !ok {
							continue
						}

						name, _ := tcMap["name"].(string)
						if name == "" {
							name, _ = tcMap["tool"].(string)
						}

						isMod := IsFileModifyingTool(name)
						isRead := IsFileReadingTool(name)
						if !isMod && !isRead {
							continue
						}

						// Check if tool call has its own specific model override
						tcModel := currentModel
						if m := extractModelFromRow(tcMap); m != "" {
							tcModel = m
						}

						// Extract args
						var args map[string]interface{}
						if a, ok := tcMap["args"].(map[string]interface{}); ok {
							args = a
						} else if a, ok := tcMap["parameters"].(map[string]interface{}); ok {
							args = a
						}

						targetFile := ExtractTargetFile(args)
						cost := models.Global.CalculateCost(tcModel, promptTokens, completionTokens)

						occurredAt := time.Now().UTC()
						if created, ok := row["created_at"].(string); ok {
							if t, err := time.Parse(time.RFC3339, created); err == nil {
								occurredAt = t
							}
						}

						if isMod {
							ev := ToolCallEvent{
								SessionID:        sessionID,
								AgentName:        agentName,
								ModelName:        tcModel,
								ToolName:         name,
								TargetFile:       targetFile,
								PromptTokens:     promptTokens,
								CompletionTokens: completionTokens,
								CostUSD:          cost,
								Intent:           currentIntent,
								OccurredAt:       occurredAt,
							}
							modEvents = append(modEvents, ev)
						}

						if isRead && targetFile != "" {
							sLine, eLine, lCount := ExtractLineRange(args)
							rEv := FileReadEvent{
								ReadID:         fmt.Sprintf("read-%s-%d", sessionID, len(readEvents)+1),
								SessionID:      sessionID,
								FilePath:       targetFile,
								AgentName:      agentName,
								ModelName:      tcModel,
								ToolName:       name,
								StartLine:      sLine,
								EndLine:        eLine,
								LinesReadCount: lCount,
								PromptTokens:   promptTokens,
								CostUSD:        cost,
								Intent:         currentIntent,
								OccurredAt:     occurredAt,
							}
							readEvents = append(readEvents, rEv)
						}
					}
				}
			}
		}

		if err != nil {
			if err == io.EOF {
				break
			}
			return modEvents, readEvents, currentOffset, err
		}
	}

	return modEvents, readEvents, currentOffset, nil
}

// ParseClineTask parses a Cline / Roo Code task JSON structure.
func ParseClineTask(filePath string) ([]ToolCallEvent, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read cline task: %w", err)
	}

	var root map[string]interface{}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("unmarshal cline task: %w", err)
	}

	sessionID := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	modelName := "unknown-model"
	if m := extractModelFromRow(root); m != "" {
		modelName = m
	}

	promptTokens := extractInt64(root, "tokensIn", "promptTokens", "input_tokens")
	completionTokens := extractInt64(root, "tokensOut", "completionTokens", "output_tokens")
	cost := models.Global.CalculateCost(modelName, promptTokens, completionTokens)

	var events []ToolCallEvent
	if messages, ok := root["messages"].([]interface{}); ok {
		for _, msg := range messages {
			mMap, ok := msg.(map[string]interface{})
			if !ok {
				continue
			}
			say, _ := mMap["say"].(string)
			if say == "tool" || say == "command" {
				text, _ := mMap["text"].(string)
				events = append(events, ToolCallEvent{
					SessionID:        sessionID,
					AgentName:        "Cline/Roo",
					ModelName:        modelName,
					ToolName:         say,
					TargetFile:       text,
					PromptTokens:     promptTokens,
					CompletionTokens: completionTokens,
					CostUSD:          cost,
					OccurredAt:       time.Now().UTC(),
				})
			}
		}
	}

	return events, nil
}

// ParseAiderHistory parses an Aider Markdown history log.
func ParseAiderHistory(filePath string) ([]ToolCallEvent, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read aider history: %w", err)
	}

	content := string(data)
	var events []ToolCallEvent

	modelRe := regexp.MustCompile(`(?i)Model:\s*([\w\.\-]+)`)
	fileRe := regexp.MustCompile(`(?i)(?:Applied edit to|Updated|Created|Modified)\s*([\w\.\/\\]+)`)

	model := "unknown-model"
	if match := modelRe.FindStringSubmatch(content); len(match) > 1 {
		model = match[1]
	}

	fileMatches := fileRe.FindAllStringSubmatch(content, -1)
	for _, fm := range fileMatches {
		if len(fm) > 1 {
			events = append(events, ToolCallEvent{
				SessionID:  "aider-session",
				AgentName:  "Aider",
				ModelName:  model,
				ToolName:   "apply_diff",
				TargetFile: fm[1],
				CostUSD:    0.0,
				OccurredAt: time.Now().UTC(),
			})
		}
	}

	return events, nil
}

var (
	modelSelectionRe = regexp.MustCompile(`(?i)(?:Model Selection|Active Model)[\x60'\s:]+(?:from\s+[^\n]+?\s+)?to\s+([A-Za-z0-9\.\-_ ]+?)(?:\s*\(|\n|$)`)
	modelTagRe       = regexp.MustCompile(`(?i)<(?:model|model_name)>([^<]+)</(?:model|model_name)>|<!--\s*model:\s*([a-zA-Z0-9\.\-_/]+)\s*-->`)
)

func extractModelFromRow(m map[string]interface{}) string {
	keys := []string{
		"model", "model_name", "modelName", "model_id", "modelId",
		"apiModelId", "selectedModel", "planner_model", "llm_model", "wire_model",
	}
	for _, k := range keys {
		if val, ok := m[k]; ok {
			if s, isStr := val.(string); isStr && s != "" && s != "inherit" {
				if norm := normalizeModelName(s); norm != "" {
					return norm
				}
			}
		}
	}

	nestedKeys := []string{"metadata", "params", "options", "config", "response", "system_info", "args"}
	for _, nk := range nestedKeys {
		if sub, ok := m[nk].(map[string]interface{}); ok {
			for _, k := range keys {
				if val, ok := sub[k]; ok {
					if s, isStr := val.(string); isStr && s != "" && s != "inherit" {
						if norm := normalizeModelName(s); norm != "" {
							return norm
						}
					}
				}
			}
			if val, ok := sub["Model"]; ok {
				if s, isStr := val.(string); isStr && s != "" && s != "inherit" {
					if norm := normalizeModelName(s); norm != "" {
						return norm
					}
				}
			}
		}
	}

	// Extract from content text (e.g. Antigravity settings changes or prompt headers)
	if content, ok := m["content"].(string); ok && content != "" {
		if match := modelSelectionRe.FindStringSubmatch(content); len(match) > 1 {
			extracted := strings.TrimSpace(match[1])
			if extracted != "" && !strings.EqualFold(extracted, "none") {
				if norm := normalizeModelName(extracted); norm != "" {
					return norm
				}
			}
		}
		if match := modelTagRe.FindStringSubmatch(content); len(match) > 0 {
			for i := 1; i < len(match); i++ {
				if match[i] != "" {
					extracted := strings.TrimSpace(match[i])
					if norm := normalizeModelName(extracted); norm != "" {
						return norm
					}
				}
			}
		}
	}

	return ""
}

func normalizeModelName(raw string) string {
	raw = strings.TrimSpace(raw)
	if models.IsJunkModel(raw) {
		return ""
	}
	lower := strings.ToLower(raw)

	// Clean trailing annotations like "(Medium)" or "(Default)"
	if idx := strings.Index(raw, "("); idx > 0 {
		raw = strings.TrimSpace(raw[:idx])
		lower = strings.ToLower(raw)
	}

	// Canonical mapping for common display names
	switch {
	case strings.Contains(lower, "gemini 3.7 flash"):
		return "gemini-3.7-flash"
	case strings.Contains(lower, "gemini 2.5 pro"):
		return "gemini-2.5-pro"
	case strings.Contains(lower, "gemini 2.0 flash"):
		return "gemini-2.0-flash"
	case strings.Contains(lower, "gemini 1.5 pro"):
		return "gemini-1.5-pro"
	case strings.Contains(lower, "claude 3.7 sonnet") || strings.Contains(lower, "claude-3-7-sonnet"):
		return "claude-3-7-sonnet"
	case strings.Contains(lower, "claude 3.5 sonnet") || strings.Contains(lower, "claude-3-5-sonnet"):
		return "claude-3-5-sonnet"
	case strings.Contains(lower, "deepseek v3") || strings.Contains(lower, "deepseek-v3"):
		return "deepseek-v3"
	case strings.Contains(lower, "deepseek r1") || strings.Contains(lower, "deepseek-r1"):
		return "deepseek-r1"
	case strings.Contains(lower, "gpt-4o"):
		return "gpt-4o"
	case strings.Contains(lower, "o3-mini"):
		return "o3-mini"
	default:
		// Slugify human readable string: "Gemini Pro" -> "gemini-pro"
		slug := strings.ReplaceAll(lower, " ", "-")
		if models.IsJunkModel(slug) {
			return ""
		}
		return slug
	}
}

func extractInt64(m map[string]interface{}, keys ...string) int64 {
	for _, k := range keys {
		if val, ok := m[k]; ok {
			switch v := val.(type) {
			case float64:
				return int64(v)
			case int64:
				return v
			case int:
				return int64(v)
			}
		}
	}
	return 0
}

func detectAgentFromPath(p string) string {
	lower := strings.ToLower(p)
	switch {
	case strings.Contains(lower, "wrongstack"):
		return "WrongStack"
	case strings.Contains(lower, "antigravity") || strings.Contains(lower, "gemini"):
		return "Antigravity"
	case strings.Contains(lower, "claude"):
		return "Claude Code"
	case strings.Contains(lower, "cline") || strings.Contains(lower, "roo"):
		return "Cline/Roo"
	case strings.Contains(lower, "replit"):
		return "Replit Agent"
	case strings.Contains(lower, "zed"):
		return "Zed AI"
	case strings.Contains(lower, "zcode") || strings.Contains(lower, "z.ai"):
		return "ZCode"
	case strings.Contains(lower, "minimax") || strings.Contains(lower, "abab"):
		return "MiniMax Code"
	case strings.Contains(lower, "kimi") || strings.Contains(lower, "moonshot"):
		return "Kimi Code"
	case strings.Contains(lower, "devin"):
		return "Devin"
	case strings.Contains(lower, "trae"):
		return "Trae"
	case strings.Contains(lower, "copilot") || strings.Contains(lower, "github-copilot"):
		return "GitHub Copilot"
	case strings.Contains(lower, "openhands") || strings.Contains(lower, "opendevin"):
		return "OpenHands"
	case strings.Contains(lower, "goose"):
		return "Goose"
	case strings.Contains(lower, "cursor"):
		return "Cursor"
	case strings.Contains(lower, "windsurf") || strings.Contains(lower, "codeium"):
		return "Windsurf"
	case strings.Contains(lower, "aider"):
		return "Aider"
	case strings.Contains(lower, "continue"):
		return "Continue.dev"
	case strings.Contains(lower, "tabnine"):
		return "Tabnine"
	case strings.Contains(lower, "bolt"):
		return "Bolt.new"
	case strings.Contains(lower, "lovable"):
		return "Lovable"
	case strings.Contains(lower, "v0"):
		return "v0.dev"
	case strings.Contains(lower, "plandex"):
		return "Plandex"
	case strings.Contains(lower, "sweep"):
		return "Sweep"
	default:
		return "Coding Agent"
	}
}

func detectAgentDefaultModel(agentName string) string {
	lower := strings.ToLower(agentName)
	switch {
	case strings.Contains(lower, "antigravity") || strings.Contains(lower, "gemini"):
		return "gemini-3.7-flash"
	case strings.Contains(lower, "claude"):
		return "claude-3-7-sonnet"
	case strings.Contains(lower, "aider"):
		return "gpt-4o"
	case strings.Contains(lower, "cline") || strings.Contains(lower, "roo"):
		return "claude-3-7-sonnet"
	case strings.Contains(lower, "cursor") || strings.Contains(lower, "windsurf") || strings.Contains(lower, "trae") || strings.Contains(lower, "wrongstack"):
		return "claude-3-7-sonnet"
	default:
		return "claude-3-7-sonnet"
	}
}

