package proxy

import (
	"encoding/json"
	"sort"
	"strings"
)

type wireResponsesText struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
}

type wireResponsesItem struct {
	Type      string              `json:"type"`
	ID        string              `json:"id"`
	CallID    string              `json:"call_id"`
	Name      string              `json:"name"`
	Arguments string              `json:"arguments"`
	Input     string              `json:"input"`
	Content   []wireResponsesText `json:"content"`
	Summary   []wireResponsesText `json:"summary"`
}

type wireResponsesResponse struct {
	ID                string              `json:"id"`
	Model             string              `json:"model"`
	Status            string              `json:"status"`
	Output            []wireResponsesItem `json:"output"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Usage *struct {
		InputTokens  *int64 `json:"input_tokens"`
		OutputTokens *int64 `json:"output_tokens"`
		TotalTokens  *int64 `json:"total_tokens"`
		InputDetails struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"input_tokens_details"`
		OutputDetails struct {
			ReasoningTokens int64 `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
	} `json:"usage"`
}

func (pa *PayloadAnalysis) applyResponsesMetadata(response wireResponsesResponse) {
	if response.ID != "" {
		pa.WireID = response.ID
	}
	if response.Model != "" {
		pa.WireModel = response.Model
	}
	switch response.Status {
	case "completed", "failed", "incomplete", "cancelled":
		pa.FinishReason = response.Status
		if response.IncompleteDetails != nil && response.IncompleteDetails.Reason != "" {
			pa.FinishReason = response.IncompleteDetails.Reason
		}
	}
	if usage := response.Usage; usage != nil {
		if usage.InputTokens != nil {
			pa.PromptTokens = *usage.InputTokens
			pa.promptTokensReported = true
		}
		if usage.OutputTokens != nil {
			pa.CompletionTokens = *usage.OutputTokens
			pa.completionTokensReported = true
		}
		// Responses input_tokens already includes cached input. Anthropic's
		// additive cache accounting must not be applied to this shape.
		pa.CachedTokens = usage.InputDetails.CachedTokens
		pa.ReasoningTokens = usage.OutputDetails.ReasoningTokens
		pa.TotalTokens = pa.PromptTokens + pa.CompletionTokens
		if usage.TotalTokens != nil {
			pa.TotalTokens = *usage.TotalTokens
		}
	}
}

func (pa *PayloadAnalysis) applyResponsesResponse(response wireResponsesResponse) {
	pa.applyResponsesMetadata(response)
	var reply, reasoning []string
	pa.ToolCalls = nil
	for _, item := range response.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				if part.Text != "" {
					reply = append(reply, part.Text)
				} else if part.Refusal != "" {
					reply = append(reply, part.Refusal)
				}
			}
		case "reasoning":
			for _, part := range item.Summary {
				if part.Text != "" {
					reasoning = append(reasoning, part.Text)
				}
			}
		case "function_call", "custom_tool_call":
			if item.Name == "" {
				continue
			}
			id := item.CallID
			if id == "" {
				id = item.ID
			}
			args := item.Arguments
			if item.Type == "custom_tool_call" {
				args = item.Input
			}
			pa.ToolCalls = append(pa.ToolCalls, ProxyToolCall{ID: id, Name: item.Name, Arguments: args, TargetFile: extractFileFromArgsString(args)})
		}
	}
	pa.AssistantReply = strings.Join(reply, "\n")
	pa.Reasoning = strings.Join(reasoning, "\n")
}

type responsesStreamItem struct {
	item    wireResponsesItem
	args    strings.Builder
	text    responsesStreamText
	summary responsesStreamText
}

type responsesStreamText map[int]*strings.Builder

func (text responsesStreamText) write(index int, value string, replace bool) {
	if text[index] == nil {
		text[index] = &strings.Builder{}
	}
	if replace {
		text[index].Reset()
	}
	text[index].WriteString(value)
}

type responsesStreamState struct {
	items map[int]*responsesStreamItem
}

func (s *responsesStreamState) item(index int) *responsesStreamItem {
	if s.items == nil {
		s.items = make(map[int]*responsesStreamItem)
	}
	if s.items[index] == nil {
		s.items[index] = &responsesStreamItem{text: make(responsesStreamText), summary: make(responsesStreamText)}
	}
	return s.items[index]
}

func (s *responsesStreamState) setItem(index int, item wireResponsesItem) {
	buf := &responsesStreamItem{item: item, text: make(responsesStreamText), summary: make(responsesStreamText)}
	buf.args.WriteString(item.Arguments)
	if item.Type == "custom_tool_call" {
		buf.args.Reset()
		buf.args.WriteString(item.Input)
	}
	for i, part := range item.Content {
		text := part.Text
		if part.Refusal != "" {
			text = part.Refusal
		}
		buf.text.write(i, text, true)
	}
	for i, part := range item.Summary {
		buf.summary.write(i, part.Text, true)
	}
	if s.items == nil {
		s.items = make(map[int]*responsesStreamItem)
	}
	s.items[index] = buf
}

func (s *responsesStreamState) handle(pa *PayloadAnalysis, chunk sseStreamChunk) {
	if len(chunk.Response) > 0 {
		var response wireResponsesResponse
		if json.Unmarshal(chunk.Response, &response) == nil {
			pa.applyResponsesMetadata(response)
			if response.Output != nil {
				s.items = make(map[int]*responsesStreamItem)
				for i, item := range response.Output {
					s.setItem(i, item)
				}
			}
		}
	}
	switch chunk.Type {
	case "response.output_item.added", "response.output_item.done":
		if chunk.Item != nil {
			s.setItem(chunk.OutputIndex, *chunk.Item)
		}
	case "response.output_text.delta", "response.refusal.delta":
		if chunk.Delta != nil {
			buf := s.item(chunk.OutputIndex)
			buf.item.Type = "message"
			buf.text.write(chunk.ContentIndex, chunk.Delta.ResponseText, false)
		}
	case "response.output_text.done", "response.refusal.done":
		buf := s.item(chunk.OutputIndex)
		buf.item.Type = "message"
		text := chunk.Text
		if chunk.Type == "response.refusal.done" {
			text = chunk.Refusal
		}
		buf.text.write(chunk.ContentIndex, text, true)
	case "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done":
		buf := s.item(chunk.OutputIndex)
		buf.item.Type = "reasoning"
		if chunk.Type == "response.reasoning_summary_text.done" {
			buf.summary.write(chunk.SummaryIndex, chunk.Text, true)
		} else if chunk.Delta != nil {
			buf.summary.write(chunk.SummaryIndex, chunk.Delta.ResponseText, false)
		}
	case "response.function_call_arguments.delta", "response.function_call_arguments.done",
		"response.custom_tool_call_input.delta", "response.custom_tool_call_input.done":
		buf := s.item(chunk.OutputIndex)
		buf.item.Type = "function_call"
		if strings.HasPrefix(chunk.Type, "response.custom_tool_call_input.") {
			buf.item.Type = "custom_tool_call"
		}
		if chunk.Name != "" {
			buf.item.Name = chunk.Name
		}
		if strings.HasSuffix(chunk.Type, ".done") {
			buf.args.Reset()
			args := chunk.Arguments
			if buf.item.Type == "custom_tool_call" {
				args, _ = rawJSONString(chunk.Input)
			}
			buf.args.WriteString(args)
		} else if chunk.Delta != nil {
			buf.args.WriteString(chunk.Delta.ResponseText)
		}
	}
}

func sortedResponsesIndices[V any](items map[int]V) []int {
	indices := make([]int, 0, len(items))
	for index := range items {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	return indices
}

func (s *responsesStreamState) finish(pa *PayloadAnalysis) {
	if s.items == nil {
		return
	}
	var output []wireResponsesItem
	for _, index := range sortedResponsesIndices(s.items) {
		buf := s.items[index]
		item := buf.item
		item.Arguments = buf.args.String()
		if item.Type == "custom_tool_call" {
			item.Input = buf.args.String()
		}
		item.Content, item.Summary = nil, nil
		for _, i := range sortedResponsesIndices(buf.text) {
			item.Content = append(item.Content, wireResponsesText{Text: buf.text[i].String()})
		}
		for _, i := range sortedResponsesIndices(buf.summary) {
			item.Summary = append(item.Summary, wireResponsesText{Text: buf.summary[i].String()})
		}
		output = append(output, item)
	}
	pa.applyResponsesResponse(wireResponsesResponse{Output: output})
}

func responsesInputText(content json.RawMessage) string {
	if text, ok := rawJSONString(content); ok {
		return text
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if !decodeLenient(content, &blocks) {
		return ""
	}
	var texts []string
	for _, block := range blocks {
		if block.Text != "" {
			texts = append(texts, block.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func summarizeResponsesInput(sum *wireRequestSummary, instructions string, input json.RawMessage) {
	sum.charCount += len(instructions)
	if sum.systemPrompt == "" && instructions != "" {
		sum.systemPrompt = runeSafeTruncate(instructions, 120)
	}
	setIntent := func(text string) {
		sum.userIntent = runeSafePrefix(text, 80)
		if len(text) > len(sum.userIntent) {
			sum.userIntent += "…"
		}
	}
	if text, ok := rawJSONString(input); ok {
		sum.messageCount++
		sum.charCount += len(text)
		setIntent(text)
		return
	}
	var items []struct {
		Role      string          `json:"role"`
		Content   json.RawMessage `json:"content"`
		Arguments string          `json:"arguments"`
		Output    json.RawMessage `json:"output"`
	}
	if !decodeLenient(input, &items) {
		return
	}
	sum.messageCount += len(items)
	for _, item := range items {
		text := responsesInputText(item.Content)
		sum.charCount += contentChars(item.Content) + len(item.Arguments) + contentChars(item.Output)
		if sum.systemPrompt == "" && (item.Role == "system" || item.Role == "developer") {
			sum.systemPrompt = runeSafeTruncate(text, 120)
		}
		if item.Role == "user" {
			setIntent(text)
		}
	}
}
