package proxy

import (
	"strings"
	"testing"
)

func TestAnalyzeWirePayloads_OpenAIToolCalling(t *testing.T) {
	req := `{
		"model": "gpt-4o",
		"messages": [
			{"role": "system", "content": "You are an expert AI engineer."},
			{"role": "user", "content": "Fix the bug in internal/core/atlas.go"}
		]
	}`

	resp := `{
		"choices": [{
			"finish_reason": "tool_calls",
			"message": {
				"role": "assistant",
				"content": "I will examine the file now.",
				"tool_calls": [{
					"id": "call_123",
					"type": "function",
					"function": {
						"name": "view_file",
						"arguments": "{\"AbsolutePath\":\"D:/Codebox/PROJECTS/WrongTrace/internal/core/atlas.go\",\"StartLine\":1}"
					}
				}]
			}
		}]
	}`

	analysis := AnalyzeWirePayloads([]byte(req), []byte(resp), false)

	if analysis.MessageCount != 2 {
		t.Errorf("expected 2 messages, got %d", analysis.MessageCount)
	}
	if analysis.SystemPrompt != "You are an expert AI engineer." {
		t.Errorf("system prompt = %q", analysis.SystemPrompt)
	}
	if analysis.AssistantReply != "I will examine the file now." {
		t.Errorf("assistant reply = %q", analysis.AssistantReply)
	}
	if analysis.FinishReason != "tool_calls" {
		t.Errorf("finish reason = %q", analysis.FinishReason)
	}
	if len(analysis.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(analysis.ToolCalls))
	}
	tc := analysis.ToolCalls[0]
	if tc.Name != "view_file" || tc.ID != "call_123" || tc.TargetFile != "D:/Codebox/PROJECTS/WrongTrace/internal/core/atlas.go" {
		t.Errorf("unexpected tool call: %+v", tc)
	}
}

func TestAnalyzeWirePayloads_AnthropicToolUse(t *testing.T) {
	req := `{
		"model": "claude-3-7-sonnet-20250219",
		"system": "You are Claude assistant.",
		"messages": [{"role": "user", "content": "Edit main.go"}]
	}`

	resp := `{
		"stop_reason": "tool_use",
		"content": [
			{"type": "text", "text": "Editing main.go now."},
			{
				"type": "tool_use",
				"id": "toolu_456",
				"name": "replace_file_content",
				"input": {
					"target_file": "cmd/wrongtrace/main.go",
					"replacement": "package main"
				}
			}
		]
	}`

	analysis := AnalyzeWirePayloads([]byte(req), []byte(resp), false)
	if analysis.SystemPrompt != "You are Claude assistant." {
		t.Errorf("system prompt = %q", analysis.SystemPrompt)
	}
	if analysis.AssistantReply != "Editing main.go now." {
		t.Errorf("assistant reply = %q", analysis.AssistantReply)
	}
	if len(analysis.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(analysis.ToolCalls))
	}
	if analysis.ToolCalls[0].TargetFile != "cmd/wrongtrace/main.go" || analysis.ToolCalls[0].Name != "replace_file_content" {
		t.Errorf("unexpected anthropic tool call: %+v", analysis.ToolCalls[0])
	}
}

func TestAnalyzeWirePayloads_ReasoningAndStream(t *testing.T) {
	req := `{"messages":[{"role":"user","content":"Think carefully"}]}`
	resp := `<think>Let's check the code.</think>The solution is simple.`

	analysis := AnalyzeWirePayloads([]byte(req), []byte(resp), false)
	if analysis.Reasoning != "Let's check the code." {
		t.Errorf("reasoning = %q", analysis.Reasoning)
	}
	if analysis.AssistantReply != "The solution is simple." {
		t.Errorf("reply = %q", analysis.AssistantReply)
	}
}

func TestAnalyzeWirePayloads_GLMRealStreamChunk(t *testing.T) {
	req := `{"model":"glm-5.3","messages":[{"role":"user","content":"Run tool"}]}`
	chunk := `data: {"id":"2026082302422025cef109e41f480f","created":1787424140,"object":"chat.completion.chunk","model":"glm-5.3","choices":[{"index":0,"finish_reason":"tool_calls","delta":{"role":"assistant","content":""}}],"usage":{"prompt_tokens":24636,"completion_tokens":282,"total_tokens":24918,"prompt_tokens_details":{"cached_tokens":20160},"completion_tokens_details":{"reasoning_tokens":231}}}`

	analysis := AnalyzeWirePayloads([]byte(req), []byte(chunk), true)

	if analysis.WireModel != "glm-5.3" {
		t.Errorf("wire model = %q, want glm-5.3", analysis.WireModel)
	}
	if analysis.WireID != "2026082302422025cef109e41f480f" {
		t.Errorf("wire id = %q", analysis.WireID)
	}
	if analysis.PromptTokens != 24636 || analysis.CompletionTokens != 282 || analysis.TotalTokens != 24918 {
		t.Errorf("tokens mismatch: prompt=%d, completion=%d, total=%d", analysis.PromptTokens, analysis.CompletionTokens, analysis.TotalTokens)
	}
	if analysis.CachedTokens != 20160 {
		t.Errorf("cached tokens = %d, want 20160", analysis.CachedTokens)
	}
	if analysis.ReasoningTokens != 231 {
		t.Errorf("reasoning tokens = %d, want 231", analysis.ReasoningTokens)
	}
	if analysis.FinishReason != "tool_calls" {
		t.Errorf("finish reason = %q, want tool_calls", analysis.FinishReason)
	}
}

func TestAnalyzeWirePayloads_AnthropicStreamUsage(t *testing.T) {
	req := `{"model":"claude-3-7-sonnet","messages":[{"role":"user","content":"Hello"}]}`
	stream := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_123\",\"model\":\"claude-3-7-sonnet\",\"usage\":{\"input_tokens\":1100,\"cache_read_input_tokens\":4100}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":140}}\n\n"

	analysis := AnalyzeWirePayloads([]byte(req), []byte(stream), true)

	if analysis.WireID != "msg_123" {
		t.Errorf("wire id = %q, want msg_123", analysis.WireID)
	}
	if analysis.PromptTokens != 5200 { // 1100 + 4100
		t.Errorf("prompt tokens = %d, want 5200 (1100 non-cached + 4100 cached)", analysis.PromptTokens)
	}
	if analysis.CachedTokens != 4100 {
		t.Errorf("cached tokens = %d, want 4100", analysis.CachedTokens)
	}
	if analysis.CompletionTokens != 140 {
		t.Errorf("completion tokens = %d, want 140", analysis.CompletionTokens)
	}
}

func TestAnalyzeWirePayloads_MiniMaxAnthropicDeltaUsage(t *testing.T) {
	req := `{"model":"minimax-text-01","messages":[{"role":"user","content":"Run codebase search"}]}`
	stream := "event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"tool_use\"},\"type\":\"message_delta\",\"usage\":{\"cache_read_input_tokens\":134543,\"input_tokens\":1353,\"output_tokens\":708,\"service_tier\":\"standard\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\ndata: [DONE]\n"

	analysis := AnalyzeWirePayloads([]byte(req), []byte(stream), true)

	expectedPrompt := int64(1353 + 134543) // 135,896 total context tokens
	if analysis.PromptTokens != expectedPrompt {
		t.Errorf("prompt tokens = %d, want %d", analysis.PromptTokens, expectedPrompt)
	}
	if analysis.CachedTokens != 134543 {
		t.Errorf("cached tokens = %d, want 134543", analysis.CachedTokens)
	}
	if analysis.CompletionTokens != 708 {
		t.Errorf("completion tokens = %d, want 708", analysis.CompletionTokens)
	}
	if analysis.FinishReason != "tool_use" {
		t.Errorf("finish reason = %q, want tool_use", analysis.FinishReason)
	}
}

func TestAnalyzeWirePayloads_FallbackPromptTokenEstimation(t *testing.T) {
	longPrompt := strings.Repeat("This is a detailed prompt with code and AST instructions. ", 200) // ~11,600 chars
	req := `{"model":"gpt-4o","messages":[{"role":"user","content":"` + longPrompt + `"}]}`
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"Understood.\"}}]}\n\ndata: [DONE]\n\n"

	analysis := AnalyzeWirePayloads([]byte(req), []byte(stream), true)

	if analysis.PromptTokens == 1000 {
		t.Errorf("prompt tokens should not be hardcoded 1000 fallback, got %d", analysis.PromptTokens)
	}
	if analysis.PromptTokens < 2500 {
		t.Errorf("expected estimated prompt tokens > 2500, got %d", analysis.PromptTokens)
	}
	if analysis.AssistantReply != "Understood." {
		t.Errorf("assistant reply = %q", analysis.AssistantReply)
	}
}
