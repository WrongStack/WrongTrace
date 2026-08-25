package proxy

import (
	"bytes"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

// ProxyToolCall represents an individual tool/function invocation detected on the wire.
type ProxyToolCall struct {
	ID         string `json:"id,omitempty"`
	Name       string `json:"name"`
	TargetFile string `json:"target_file,omitempty"`
	Arguments  string `json:"arguments,omitempty"`
}

// PayloadAnalysis holds rich structured semantics extracted from intercepted LLM payloads.
type PayloadAnalysis struct {
	ToolCalls        []ProxyToolCall
	ToolCount        int
	AssistantReply   string
	Reasoning        string
	SystemPrompt     string
	MessageCount     int
	FinishReason     string
	WireModel        string
	WireID           string
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	CachedTokens     int64
	ReasoningTokens  int64
}

var (
	thinkRegex      = regexp.MustCompile(`(?s)<think>(.*?)</think>`)
	fileTargetRegex = regexp.MustCompile(`"(?:path|file|target_file|TargetFile|filePath|filename)":\s*"([^"]+)"`)
)

// AnalyzeWirePayloads extracts tool calls, reasoning/thinking blocks, assistant replies, and conversation stats.
func AnalyzeWirePayloads(reqBody, respBody []byte, isStream bool) PayloadAnalysis {
	var analysis PayloadAnalysis

	// 1. Analyze Request
	if len(reqBody) > 0 {
		var reqMap map[string]interface{}
		if err := json.Unmarshal(reqBody, &reqMap); err == nil {
			if msgs, ok := reqMap["messages"].([]interface{}); ok {
				analysis.MessageCount = len(msgs)
				for _, m := range msgs {
					if mMap, ok := m.(map[string]interface{}); ok {
						role, _ := mMap["role"].(string)
						if role == "system" && analysis.SystemPrompt == "" {
							if sysContent, ok := mMap["content"].(string); ok {
								analysis.SystemPrompt = runeSafeTruncate(sysContent, 120)
							}
						}
					}
				}
			}

			// Anthropic top-level system prompt
			if sys, ok := reqMap["system"].(string); ok && analysis.SystemPrompt == "" {
				analysis.SystemPrompt = runeSafeTruncate(sys, 120)
			}
		}
	}

	// 2. Analyze Response
	if len(respBody) > 0 {
		if !isStream {
			if !analysis.parseJSONResponse(respBody) {
				analysis.AssistantReply = string(respBody)
			}
			if analysis.PromptTokens == 0 && len(reqBody) > 0 {
				analysis.PromptTokens = EstimatePromptTokens(reqBody)
			}
			if analysis.CompletionTokens == 0 && len(analysis.AssistantReply) > 0 {
				analysis.CompletionTokens = int64(float64(len(analysis.AssistantReply)) / 3.7)
			}
			if analysis.TotalTokens == 0 {
				analysis.TotalTokens = analysis.PromptTokens + analysis.CompletionTokens
			}
		} else {
			analysis.parseSSEResponse(respBody, reqBody)
		}
	}

	// Extract <think> reasoning tags from assistant reply if present
	if strings.Contains(analysis.AssistantReply, "<think>") {
		if matches := thinkRegex.FindStringSubmatch(analysis.AssistantReply); len(matches) > 1 {
			analysis.Reasoning = strings.TrimSpace(matches[1])
			analysis.AssistantReply = strings.TrimSpace(thinkRegex.ReplaceAllString(analysis.AssistantReply, ""))
		}
	}

	analysis.ToolCount = len(analysis.ToolCalls)
	return analysis
}

func (pa *PayloadAnalysis) parseJSONResponse(data []byte) bool {
	var respMap map[string]interface{}
	if err := json.Unmarshal(data, &respMap); err != nil {
		return false
	}

	if id, ok := respMap["id"].(string); ok && id != "" {
		pa.WireID = id
	}
	if model, ok := respMap["model"].(string); ok && model != "" {
		pa.WireModel = model
	}

	// Parse Usage Details
	if usageMap, ok := respMap["usage"].(map[string]interface{}); ok {
		pa.extractUsage(usageMap)
	}

	// OpenAI format
	if choices, ok := respMap["choices"].([]interface{}); ok && len(choices) > 0 {
		if firstChoice, ok := choices[0].(map[string]interface{}); ok {
			if fr, ok := firstChoice["finish_reason"].(string); ok {
				pa.FinishReason = fr
			}
			if msg, ok := firstChoice["message"].(map[string]interface{}); ok {
				if content, ok := msg["content"].(string); ok {
					pa.AssistantReply = content
				}
				if rContent, ok := msg["reasoning_content"].(string); ok && rContent != "" {
					pa.Reasoning = rContent
				}

				// OpenAI tool_calls array
				if toolCalls, ok := msg["tool_calls"].([]interface{}); ok {
					for _, tc := range toolCalls {
						if tcMap, ok := tc.(map[string]interface{}); ok {
							callID, _ := tcMap["id"].(string)
							var fnName, fnArgs string
							if fnMap, ok := tcMap["function"].(map[string]interface{}); ok {
								fnName, _ = fnMap["name"].(string)
								fnArgs, _ = fnMap["arguments"].(string)
							}
							if fnName == "" {
								fnName, _ = tcMap["name"].(string)
							}

							targetFile := extractFileFromArgsString(fnArgs)
							pa.ToolCalls = append(pa.ToolCalls, ProxyToolCall{
								ID:         callID,
								Name:       fnName,
								TargetFile: targetFile,
								Arguments:  fnArgs,
							})
						}
					}
				}
			}
		}
		return true
	}

	// Anthropic format
	if contentArr, ok := respMap["content"].([]interface{}); ok {
		if stopReason, ok := respMap["stop_reason"].(string); ok {
			pa.FinishReason = stopReason
		}
		for _, item := range contentArr {
			if itemMap, ok := item.(map[string]interface{}); ok {
				cType, _ := itemMap["type"].(string)
				if cType == "text" {
					if t, ok := itemMap["text"].(string); ok {
						if pa.AssistantReply != "" {
							pa.AssistantReply += "\n" + t
						} else {
							pa.AssistantReply = t
						}
					}
				} else if cType == "tool_use" {
					callID, _ := itemMap["id"].(string)
					fnName, _ := itemMap["name"].(string)
					var argsStr string
					var targetFile string
					if inputMap, ok := itemMap["input"].(map[string]interface{}); ok {
						if b, err := json.Marshal(inputMap); err == nil {
							argsStr = string(b)
						}
						targetFile = extractFileFromMap(inputMap)
					}
					pa.ToolCalls = append(pa.ToolCalls, ProxyToolCall{
						ID:         callID,
						Name:       fnName,
						TargetFile: targetFile,
						Arguments:  argsStr,
					})
				}
			}
		}
	}
	return true
}

type sseToolCallItem struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type sseDeltaItem struct {
	Content          string            `json:"content"`
	ReasoningContent string            `json:"reasoning_content"`
	ToolCalls        []sseToolCallItem `json:"tool_calls"`
	Type             string            `json:"type"`
	Text             string            `json:"text"`
	PartialJSON      string            `json:"partial_json"`
	StopReason       string            `json:"stop_reason"`
}

type sseChoiceItem struct {
	Index        int          `json:"index"`
	FinishReason string       `json:"finish_reason"`
	Delta        sseDeltaItem `json:"delta"`
}

type sseStreamChunk struct {
	ID            string                 `json:"id"`
	Model         string                 `json:"model"`
	Type          string                 `json:"type"`
	Index         int                    `json:"index"`
	Choices       []sseChoiceItem        `json:"choices"`
	Usage         map[string]interface{} `json:"usage"`
	UsageMetadata map[string]interface{} `json:"usageMetadata"`
	Message       *struct {
		ID    string                 `json:"id"`
		Model string                 `json:"model"`
		Usage map[string]interface{} `json:"usage"`
	} `json:"message"`
	ContentBlock *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta *sseDeltaItem `json:"delta"`
}

func (pa *PayloadAnalysis) parseSSEResponse(data []byte, reqBody []byte) {
	var textBuilder strings.Builder
	var reasoningBuilder strings.Builder

	type toolBuffer struct {
		id   string
		name string
		args strings.Builder
	}
	toolMap := make(map[int]*toolBuffer)

	remaining := data
	for len(remaining) > 0 {
		var line []byte
		if idx := bytes.IndexByte(remaining, '\n'); idx >= 0 {
			line = remaining[:idx]
			remaining = remaining[idx+1:]
		} else {
			line = remaining
			remaining = nil
		}
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		jsonPart := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if bytes.Equal(jsonPart, []byte("[DONE]")) || len(jsonPart) == 0 {
			continue
		}

		var chunk sseStreamChunk
		if err := json.Unmarshal(jsonPart, &chunk); err != nil {
			continue
		}

		if chunk.ID != "" {
			pa.WireID = chunk.ID
		}
		if chunk.Model != "" {
			pa.WireModel = chunk.Model
		}

		// 1. Direct OpenAI / DeepSeek / Groq chunk usage
		if chunk.Usage != nil {
			pa.extractUsage(chunk.Usage)
		}

		// 2. Anthropic message_start usage
		if chunk.Message != nil {
			if chunk.Message.ID != "" {
				pa.WireID = chunk.Message.ID
			}
			if chunk.Message.Model != "" {
				pa.WireModel = chunk.Message.Model
			}
			if chunk.Message.Usage != nil {
				pa.extractUsage(chunk.Message.Usage)
			}
		}

		// 3. Gemini usageMetadata
		if chunk.UsageMetadata != nil {
			pa.extractUsage(chunk.UsageMetadata)
		}

		// OpenAI SSE format
		if len(chunk.Choices) > 0 {
			firstChoice := chunk.Choices[0]
			if firstChoice.FinishReason != "" {
				pa.FinishReason = firstChoice.FinishReason
			}
			if firstChoice.Delta.Content != "" {
				textBuilder.WriteString(firstChoice.Delta.Content)
			}
			if firstChoice.Delta.ReasoningContent != "" {
				reasoningBuilder.WriteString(firstChoice.Delta.ReasoningContent)
			}

			// SSE delta.tool_calls
			if len(firstChoice.Delta.ToolCalls) > 0 {
				for _, tc := range firstChoice.Delta.ToolCalls {
					idx := tc.Index
					buf, exists := toolMap[idx]
					if !exists {
						buf = &toolBuffer{}
						toolMap[idx] = buf
					}
					if tc.ID != "" {
						buf.id = tc.ID
					}
					if tc.Function.Name != "" {
						buf.name = tc.Function.Name
					}
					if tc.Function.Arguments != "" {
						buf.args.WriteString(tc.Function.Arguments)
					}
				}
			}
		}

		// Anthropic content_block_delta tool parsing
		if chunk.Type != "" {
			if chunk.Type == "content_block_start" && chunk.ContentBlock != nil {
				if chunk.ContentBlock.Type == "tool_use" {
					idx := chunk.Index
					toolMap[idx] = &toolBuffer{
						id:   chunk.ContentBlock.ID,
						name: chunk.ContentBlock.Name,
					}
				}
			} else if chunk.Type == "content_block_delta" && chunk.Delta != nil {
				if chunk.Delta.Type == "input_json_delta" {
					idx := chunk.Index
					if buf, exists := toolMap[idx]; exists {
						if chunk.Delta.PartialJSON != "" {
							buf.args.WriteString(chunk.Delta.PartialJSON)
						}
					}
				} else if chunk.Delta.Type == "text_delta" {
					if chunk.Delta.Text != "" {
						textBuilder.WriteString(chunk.Delta.Text)
					}
				}
			} else if chunk.Type == "message_delta" && chunk.Delta != nil {
				if chunk.Delta.StopReason != "" {
					pa.FinishReason = chunk.Delta.StopReason
				}
			}
		}
	}

	pa.AssistantReply = textBuilder.String()
	if reasoningBuilder.Len() > 0 {
		pa.Reasoning = reasoningBuilder.String()
	}

	// Assemble streamed tools (sort keys to handle non-zero based indices in Anthropic streams)
	toolIndices := make([]int, 0, len(toolMap))
	for idx := range toolMap {
		toolIndices = append(toolIndices, idx)
	}
	sort.Ints(toolIndices)

	for _, idx := range toolIndices {
		buf := toolMap[idx]
		if buf != nil && buf.name != "" {
			argStr := buf.args.String()
			targetFile := extractFileFromArgsString(argStr)
			pa.ToolCalls = append(pa.ToolCalls, ProxyToolCall{
				ID:         buf.id,
				Name:       buf.name,
				TargetFile: targetFile,
				Arguments:  argStr,
			})
		}
	}

	// If upstream stream did not emit usage metadata, compute accurate token estimate from request/reply
	if pa.PromptTokens == 0 && len(reqBody) > 0 {
		pa.PromptTokens = EstimatePromptTokens(reqBody)
	}
	if pa.CompletionTokens == 0 {
		outChars := len(pa.AssistantReply) + len(pa.Reasoning)
		for _, tc := range pa.ToolCalls {
			outChars += len(tc.Name) + len(tc.Arguments)
		}
		if outChars > 0 {
			pa.CompletionTokens = int64(float64(outChars) / 3.7)
			if pa.CompletionTokens < 1 {
				pa.CompletionTokens = 1
			}
		}
	}
	if pa.TotalTokens == 0 {
		pa.TotalTokens = pa.PromptTokens + pa.CompletionTokens
	}
}

func (pa *PayloadAnalysis) extractUsage(usageMap map[string]interface{}) {
	var cacheRead, cacheCreate int64

	// OpenAI / GLM / DeepSeek / MiniMax prompt_tokens_details
	if ptd, ok := usageMap["prompt_tokens_details"].(map[string]interface{}); ok {
		if cached, ok := ptd["cached_tokens"].(float64); ok && cached > 0 {
			cacheRead = int64(cached)
		} else if cached, ok := ptd["cache_read_tokens"].(float64); ok && cached > 0 {
			cacheRead = int64(cached)
		}
	}

	// Anthropic / MiniMax / Kimi direct cache fields
	if cacheRead == 0 {
		if cached, ok := usageMap["cache_read_input_tokens"].(float64); ok && cached > 0 {
			cacheRead = int64(cached)
		} else if cached, ok := usageMap["prompt_cache_hit_tokens"].(float64); ok && cached > 0 {
			cacheRead = int64(cached)
		} else if cached, ok := usageMap["cachedContentTokenCount"].(float64); ok && cached > 0 {
			cacheRead = int64(cached)
		} else if cached, ok := usageMap["cache_tokens"].(float64); ok && cached > 0 {
			cacheRead = int64(cached)
		} else if cached, ok := usageMap["cached_tokens"].(float64); ok && cached > 0 {
			cacheRead = int64(cached)
		}
	}

	if created, ok := usageMap["cache_creation_input_tokens"].(float64); ok && created > 0 {
		cacheCreate = int64(created)
	}

	if cacheRead > 0 {
		pa.CachedTokens = cacheRead
	}

	// Prompt / Input Tokens
	if pt, ok := usageMap["input_tokens"].(float64); ok && (pt > 0 || cacheRead > 0 || cacheCreate > 0) {
		// In Anthropic/MiniMax API specs, input_tokens is NON-cached delta.
		// Total actual prompt context sent to model is input_tokens + cache_read + cache_creation.
		pa.PromptTokens = int64(pt) + cacheRead + cacheCreate
	} else if pt, ok := usageMap["prompt_tokens"].(float64); ok {
		pa.PromptTokens = int64(pt)
		if pa.CachedTokens > pa.PromptTokens {
			// Some providers report non-cached delta as prompt_tokens while returning large cache_read
			pa.PromptTokens += pa.CachedTokens
		}
	} else if pt, ok := usageMap["promptTokenCount"].(float64); ok {
		pa.PromptTokens = int64(pt)
	} else if cacheRead > 0 {
		pa.PromptTokens = cacheRead + cacheCreate
	}

	// Completion / Output Tokens
	if ct, ok := usageMap["completion_tokens"].(float64); ok && ct > 0 {
		pa.CompletionTokens = int64(ct)
	} else if ct, ok := usageMap["output_tokens"].(float64); ok && ct > 0 {
		pa.CompletionTokens = int64(ct)
	} else if ct, ok := usageMap["candidatesTokenCount"].(float64); ok && ct > 0 {
		pa.CompletionTokens = int64(ct)
	}

	// Total Tokens
	if tt, ok := usageMap["total_tokens"].(float64); ok && tt > 0 {
		pa.TotalTokens = int64(tt)
	} else if tt, ok := usageMap["totalTokenCount"].(float64); ok && tt > 0 {
		pa.TotalTokens = int64(tt)
	}
	if pa.TotalTokens < pa.PromptTokens+pa.CompletionTokens {
		pa.TotalTokens = pa.PromptTokens + pa.CompletionTokens
	}

	// Completion reasoning tokens
	if ctd, ok := usageMap["completion_tokens_details"].(map[string]interface{}); ok {
		if reasoning, ok := ctd["reasoning_tokens"].(float64); ok {
			pa.ReasoningTokens = int64(reasoning)
		}
	}
}

// EstimatePromptTokens calculates accurate token count from request payload when upstream does not return usage.
func EstimatePromptTokens(reqBody []byte) int64 {
	if len(reqBody) == 0 {
		return 0
	}
	var reqMap map[string]interface{}
	if err := json.Unmarshal(reqBody, &reqMap); err != nil {
		return int64(float64(len(reqBody)) / 3.7)
	}

	charCount := 0

	// 1. System prompt
	if sys, ok := reqMap["system"].(string); ok {
		charCount += len(sys)
	}

	// 2. Messages
	if msgs, ok := reqMap["messages"].([]interface{}); ok {
		for _, m := range msgs {
			if mMap, ok := m.(map[string]interface{}); ok {
				if contentStr, ok := mMap["content"].(string); ok {
					charCount += len(contentStr)
				} else if contentArr, ok := mMap["content"].([]interface{}); ok {
					for _, block := range contentArr {
						if bMap, ok := block.(map[string]interface{}); ok {
							if text, ok := bMap["text"].(string); ok {
								charCount += len(text)
							}
						}
					}
				}
			}
		}
	}

	// 3. Tools definitions
	if tools, ok := reqMap["tools"].([]interface{}); ok {
		for _, t := range tools {
			if tb, err := json.Marshal(t); err == nil {
				charCount += len(tb)
			}
		}
	}

	if charCount == 0 {
		charCount = len(reqBody)
	}

	tokens := int64(float64(charCount) / 3.7)
	if tokens < 1 {
		tokens = 1
	}
	return tokens
}

func extractFileFromArgsString(args string) string {
	if args == "" {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(args), &m); err == nil {
		return extractFileFromMap(m)
	}

	// Fallback regex match for "path": "..." or "file": "..."
	if match := fileTargetRegex.FindStringSubmatch(args); len(match) > 1 {
		return match[1]
	}
	return ""
}

var extractFileKeys = []string{
	"AbsolutePath", "target_file", "TargetFile", "path", "FilePath", "file_path",
	"filename", "file", "target", "dest", "uri",
}

func extractFileFromMap(m map[string]interface{}) string {
	for _, k := range extractFileKeys {
		if val, ok := m[k]; ok {
			if s, isStr := val.(string); isStr && s != "" {
				return s
			}
		}
	}
	for k, val := range m {
		lowerK := strings.ToLower(k)
		if strings.Contains(lowerK, "file") || strings.Contains(lowerK, "path") {
			if s, isStr := val.(string); isStr && s != "" {
				return s
			}
		}
	}
	return ""
}

func runeSafeTruncate(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == maxRunes {
			return s[:i] + "…"
		}
		count++
	}
	return s
}
