package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
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
	UserIntent       string
	MessageCount     int
	FinishReason     string
	WireModel        string
	WireID           string
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	CachedTokens     int64
	ReasoningTokens  int64

	// estPromptTokens is the request-side token estimate computed during the
	// request decode, so the fallback never re-parses the body.
	estPromptTokens int64
}

// EstimatedPromptTokens returns the prompt-token estimate for reqBody, reusing
// the one computed by AnalyzeWirePayloads when available.
func (pa *PayloadAnalysis) EstimatedPromptTokens(reqBody []byte) int64 {
	if pa.estPromptTokens > 0 {
		return pa.estPromptTokens
	}
	return EstimatePromptTokens(reqBody)
}

var (
	thinkRegex      = regexp.MustCompile(`(?s)<think>(.*?)</think>`)
	fileTargetRegex = regexp.MustCompile(`"(?:path|file|target_file|TargetFile|filePath|filename)":\s*"([^"]+)"`)
)

// AnalyzeWirePayloads extracts tool calls, reasoning/thinking blocks, assistant replies, and conversation stats.
func AnalyzeWirePayloads(reqBody, respBody []byte, isStream bool) PayloadAnalysis {
	var analysis PayloadAnalysis

	// 1. Analyze Request. One typed decode serves both the conversation stats
	// and the prompt-token fallback estimate; bodies are the whole conversation
	// (often megabytes), and decoding them into map[string]interface{} two or
	// three times per exchange was the proxy's largest allocation source.
	if len(reqBody) > 0 {
		req := summarizeWireRequest(reqBody)
		analysis.MessageCount = req.messageCount
		analysis.SystemPrompt = req.systemPrompt
		analysis.UserIntent = req.userIntent
		analysis.estPromptTokens = req.estimatedTokens(len(reqBody))
	}

	// 2. Analyze Response
	if len(respBody) > 0 {
		if !isStream {
			if !analysis.parseJSONResponse(respBody) {
				analysis.AssistantReply = string(respBody)
			}
			if analysis.PromptTokens == 0 && len(reqBody) > 0 {
				analysis.PromptTokens = analysis.estPromptTokens
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

	// Gemini (generateContent) and Ollama-native non-stream bodies. Both carry
	// the same fields their stream chunks do — candidates/parts/usageMetadata/
	// modelVersion and message.content/response/prompt_eval_count/eval_count/
	// done_reason — and parseSSEResponse's handleChunk already extracts them on
	// the streaming side. Neither shape matched the OpenAI/Anthropic blocks
	// above, so the reply and the real token counts were dropped here
	// (AssistantReply empty, CompletionTokens 0, cost undercounted by
	// finalize). Decode the same chunk struct and mirror that handler's
	// extraction semantics. Bodies that took the OpenAI early return never
	// reach this, and the generic usage map was already consumed above, so
	// only the shapes it misses are handled here.
	//
	// The second decode is skipped when the body carries none of those fields
	// -- every Anthropic Messages response -- so the common non-OpenAI reply
	// is no longer decoded twice.
	if !hasNativeChunkField(respMap) {
		return true
	}
	var chunk sseStreamChunk
	if err := json.Unmarshal(data, &chunk); err == nil {
		if chunk.ModelVersion != "" {
			pa.WireModel = chunk.ModelVersion
		}
		if chunk.UsageMetadata != nil {
			pa.extractUsage(chunk.UsageMetadata)
		}
		if len(chunk.Candidates) > 0 {
			cand := chunk.Candidates[0]
			if cand.FinishReason != "" {
				pa.FinishReason = cand.FinishReason
			}
			for _, part := range cand.Content.Parts {
				if part.Thought {
					pa.Reasoning += part.Text
				} else {
					pa.AssistantReply += part.Text
				}
			}
		}
		if chunk.Message != nil {
			if len(chunk.Message.Content) > 0 && chunk.Message.Content[0] == '"' {
				var text string
				if json.Unmarshal(chunk.Message.Content, &text) == nil {
					pa.AssistantReply += text
				}
			}
			if chunk.Message.Thinking != "" {
				pa.Reasoning += chunk.Message.Thinking
			}
		}
		if len(chunk.Response) > 0 && chunk.Response[0] == '"' {
			var text string
			if json.Unmarshal(chunk.Response, &text) == nil {
				pa.AssistantReply += text
			}
		}
		if chunk.DoneReason != "" {
			pa.FinishReason = chunk.DoneReason
		}
		if chunk.PromptEvalCount > 0 {
			pa.PromptTokens = int64(chunk.PromptEvalCount)
		}
		if chunk.EvalCount > 0 {
			pa.CompletionTokens = int64(chunk.EvalCount)
		}
		if (chunk.PromptEvalCount > 0 || chunk.EvalCount > 0) && pa.TotalTokens < pa.PromptTokens+pa.CompletionTokens {
			pa.TotalTokens = pa.PromptTokens + pa.CompletionTokens
		}
	}
	return true
}

// nativeChunkFields are the top-level keys of Gemini and Ollama-native
// bodies that parseJSONResponse's sseStreamChunk pass reads.
var nativeChunkFields = []string{
	"candidates", "usageMetadata", "modelVersion", "message", "response",
	"done_reason", "prompt_eval_count", "eval_count",
}

// hasNativeChunkField reports whether m has any nativeChunkFields key.
// encoding/json matches struct fields case-insensitively, so this does too.
func hasNativeChunkField(m map[string]interface{}) bool {
	for k := range m {
		for _, f := range nativeChunkFields {
			if strings.EqualFold(k, f) {
				return true
			}
		}
	}
	return false
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
		// Content is a string in Ollama's native /api/chat chunks but an
		// array in Anthropic's message_start; RawMessage keeps either from
		// failing the whole chunk decode.
		Content  json.RawMessage `json:"content"`
		Thinking string          `json:"thinking"`
	} `json:"message"`
	// Ollama native NDJSON: /api/generate streams "response" text; the final
	// line carries done_reason and the prompt/eval token counts. Response is
	// raw because OpenAI Responses API events use "response" for an object.
	Response        json.RawMessage `json:"response"`
	DoneReason      string          `json:"done_reason"`
	PromptEvalCount float64         `json:"prompt_eval_count"`
	EvalCount       float64         `json:"eval_count"`
	// Gemini generateContent / streamGenerateContent chunks.
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text    string `json:"text"`
				Thought bool   `json:"thought"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	ModelVersion string `json:"modelVersion"`
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

	handleChunk := func(jsonPart []byte) {
		var chunk sseStreamChunk
		if err := json.Unmarshal(jsonPart, &chunk); err != nil {
			return
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

		// Gemini candidates (SSE with alt=sse, or JSON-array framing).
		if chunk.ModelVersion != "" {
			pa.WireModel = chunk.ModelVersion
		}
		if len(chunk.Candidates) > 0 {
			cand := chunk.Candidates[0]
			if cand.FinishReason != "" {
				pa.FinishReason = cand.FinishReason
			}
			for _, part := range cand.Content.Parts {
				if part.Thought {
					reasoningBuilder.WriteString(part.Text)
				} else {
					textBuilder.WriteString(part.Text)
				}
			}
		}

		// Ollama native NDJSON chunks.
		if chunk.Message != nil {
			if len(chunk.Message.Content) > 0 && chunk.Message.Content[0] == '"' {
				var text string
				if json.Unmarshal(chunk.Message.Content, &text) == nil {
					textBuilder.WriteString(text)
				}
			}
			if chunk.Message.Thinking != "" {
				reasoningBuilder.WriteString(chunk.Message.Thinking)
			}
		}
		if len(chunk.Response) > 0 && chunk.Response[0] == '"' {
			var text string
			if json.Unmarshal(chunk.Response, &text) == nil {
				textBuilder.WriteString(text)
			}
		}
		if chunk.DoneReason != "" {
			pa.FinishReason = chunk.DoneReason
		}
		if chunk.PromptEvalCount > 0 {
			pa.PromptTokens = int64(chunk.PromptEvalCount)
		}
		if chunk.EvalCount > 0 {
			pa.CompletionTokens = int64(chunk.EvalCount)
		}
		if (chunk.PromptEvalCount > 0 || chunk.EvalCount > 0) && pa.TotalTokens < pa.PromptTokens+pa.CompletionTokens {
			pa.TotalTokens = pa.PromptTokens + pa.CompletionTokens
		}
	}

	// Gemini streamGenerateContent without alt=sse streams ONE JSON array
	// whose elements are the chunks. Only a complete capture parses; a
	// head/tail-truncated one falls through to the line scan below.
	parsedArray := false
	if trimmed := bytes.TrimSpace(data); len(trimmed) > 0 && trimmed[0] == '[' {
		var elems []json.RawMessage
		if json.Unmarshal(trimmed, &elems) == nil {
			parsedArray = true
			for _, e := range elems {
				handleChunk(e)
			}
		}
	}

	remaining := data
	if parsedArray {
		remaining = nil
	}
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
		switch {
		case bytes.HasPrefix(line, []byte("data:")):
			jsonPart := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
			if bytes.Equal(jsonPart, []byte("[DONE]")) || len(jsonPart) == 0 {
				continue
			}
			handleChunk(jsonPart)
		case len(line) > 0 && line[0] == '{':
			// NDJSON (Ollama native streaming): one bare JSON object per line.
			handleChunk(line)
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
		pa.PromptTokens = pa.EstimatedPromptTokens(reqBody)
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
	return summarizeWireRequest(reqBody).estimatedTokens(len(reqBody))
}

// wireRequest is the subset of an OpenAI/Anthropic-style request the proxy
// reads. Content stays raw so large tool results and images are skipped by the
// decoder instead of being materialized as nested maps.
type wireRequest struct {
	System   json.RawMessage `json:"system"`
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	Tools []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Function    *struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"function"`
	} `json:"tools"`
}

type wireRequestSummary struct {
	valid        bool // body was syntactically valid JSON
	messageCount int
	systemPrompt string
	userIntent   string
	charCount    int
}

// estimatedTokens converts the summary's character count into a token
// estimate, falling back to the raw body size exactly as the map-based
// estimator did.
func (r wireRequestSummary) estimatedTokens(bodyLen int) int64 {
	if !r.valid {
		return int64(float64(bodyLen) / 3.7)
	}
	chars := r.charCount
	if chars == 0 {
		chars = bodyLen
	}
	tokens := int64(float64(chars) / 3.7)
	if tokens < 1 {
		tokens = 1
	}
	return tokens
}

// decodeLenient unmarshals data into v, tolerating fields whose JSON type does
// not match the target: encoding/json skips those and keeps filling the rest,
// which mirrors the type-asserting map walk this replaced.
func decodeLenient(data []byte, v any) bool {
	err := json.Unmarshal(data, v)
	if err == nil {
		return true
	}
	var typeErr *json.UnmarshalTypeError
	return errors.As(err, &typeErr)
}

// rawJSONString decodes raw when it is a JSON string.
func rawJSONString(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// contentChars counts the text characters of a message content value: a
// plain string, or an array of blocks whose "text" fields are summed.
func contentChars(raw json.RawMessage) int {
	if s, ok := rawJSONString(raw); ok {
		return len(s)
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return 0
	}
	var blocks []struct {
		Text json.RawMessage `json:"text"`
	}
	if !decodeLenient(raw, &blocks) {
		return 0
	}
	n := 0
	for _, b := range blocks {
		if s, ok := rawJSONString(b.Text); ok {
			n += len(s)
		}
	}
	return n
}

func summarizeWireRequest(body []byte) wireRequestSummary {
	var sum wireRequestSummary
	var req wireRequest
	if !decodeLenient(body, &req) {
		return sum
	}
	sum.valid = true
	sum.messageCount = len(req.Messages)

	if sys, ok := rawJSONString(req.System); ok {
		sum.charCount += len(sys)
	}
	for _, m := range req.Messages {
		content, isString := rawJSONString(m.Content)
		if isString {
			sum.charCount += len(content)
		} else {
			sum.charCount += contentChars(m.Content)
		}
		if !isString {
			continue
		}
		if m.Role == "system" && sum.systemPrompt == "" {
			sum.systemPrompt = runeSafeTruncate(content, 120)
		}
		// Last user message wins.
		if m.Role == "user" {
			sum.userIntent = runeSafePrefix(content, 80)
			if len(content) > len(sum.userIntent) {
				sum.userIntent += "…"
			}
		}
	}
	// Anthropic top-level system prompt.
	if sum.systemPrompt == "" {
		if sys, ok := rawJSONString(req.System); ok {
			sum.systemPrompt = runeSafeTruncate(sys, 120)
		}
	}
	for _, t := range req.Tools {
		if t.Function != nil {
			sum.charCount += len(t.Function.Name) + len(t.Function.Description)
		}
		sum.charCount += len(t.Name) + len(t.Description)
	}
	return sum
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
	// The fallback matches ANY key that merely mentions file/path, so several
	// keys can qualify at once. Go randomizes map iteration order per map
	// instance, so returning the first hit made the identical payload yield a
	// different TargetFile on every parse — and TargetFile drives downstream
	// project attribution. Keep the lexicographically smallest qualifying key
	// instead, so the result cannot depend on iteration order; the explicit
	// extractFileKeys list above still takes priority.
	var bestKey, bestVal string
	for k, val := range m {
		s, isStr := val.(string)
		if !isStr || s == "" {
			continue
		}
		lowerK := strings.ToLower(k)
		if !strings.Contains(lowerK, "file") && !strings.Contains(lowerK, "path") {
			continue
		}
		if bestKey == "" || k < bestKey {
			bestKey, bestVal = k, s
		}
	}
	return bestVal
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
