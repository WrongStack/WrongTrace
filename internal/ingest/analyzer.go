package ingest

import (
	"encoding/json"
	"fmt"
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
		"TargetFile", "target_file", "TargetContent",
		"path", "FilePath", "file_path", "filename", "file", "target", "dest",
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

// ParseJSONLTranscript parses a JSONL file line-by-line, extracting tool calls and token usage.
func ParseJSONLTranscript(filePath string) ([]ToolCallEvent, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read transcript: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	var events []ToolCallEvent
	sessionID := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))

	currentModel := "claude-3-7-sonnet" // default fallback
	currentIntent := ""

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		var row map[string]interface{}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}

		// Check for model or prompt intent in row
		if m, ok := row["model"].(string); ok && m != "" {
			currentModel = m
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
			promptTokens = extractInt64(usage, "input_tokens", "prompt_tokens")
			completionTokens = extractInt64(usage, "output_tokens", "completion_tokens")
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
				if !IsFileModifyingTool(name) {
					continue
				}

				// Extract args
				var args map[string]interface{}
				if a, ok := tcMap["args"].(map[string]interface{}); ok {
					args = a
				} else if a, ok := tcMap["parameters"].(map[string]interface{}); ok {
					args = a
				}

				targetFile := ExtractTargetFile(args)
				cost := models.Global.CalculateCost(currentModel, promptTokens, completionTokens)

				ev := ToolCallEvent{
					SessionID:        sessionID,
					AgentName:        detectAgentFromPath(filePath),
					ModelName:        currentModel,
					ToolName:         name,
					TargetFile:       targetFile,
					PromptTokens:     promptTokens,
					CompletionTokens: completionTokens,
					CostUSD:          cost,
					Intent:           currentIntent,
					OccurredAt:       time.Now().UTC(),
				}

				if created, ok := row["created_at"].(string); ok {
					if t, err := time.Parse(time.RFC3339, created); err == nil {
						ev.OccurredAt = t
					}
				}

				events = append(events, ev)
			}
		}
	}

	return events, nil
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
	modelName := "claude-3-5-sonnet"
	if m, ok := root["apiModelId"].(string); ok && m != "" {
		modelName = m
	}

	promptTokens := extractInt64(root, "tokensIn", "promptTokens")
	completionTokens := extractInt64(root, "tokensOut", "completionTokens")
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

	model := "deepseek-r1"
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
				CostUSD:    0.02,
				OccurredAt: time.Now().UTC(),
			})
		}
	}

	return events, nil
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
	if strings.Contains(lower, "claude") {
		return "Claude Code"
	}
	if strings.Contains(lower, "cline") || strings.Contains(lower, "roo") {
		return "Cline/Roo"
	}
	if strings.Contains(lower, "aider") {
		return "Aider"
	}
	if strings.Contains(lower, "cursor") {
		return "Cursor"
	}
	return "Agent Session"
}
