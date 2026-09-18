package proxy

import (
	"encoding/json"
	"testing"
)

// legacyRequestSummary is the map-walking request analysis the typed decode
// replaced. It is kept here only as an oracle: the single typed pass must
// produce identical stats and token estimates.
func legacyRequestSummary(body []byte) (msgs int, sysPrompt, intent string, est int64) {
	var reqMap map[string]interface{}
	if err := json.Unmarshal(body, &reqMap); err != nil {
		return 0, "", "", int64(float64(len(body)) / 3.7)
	}
	chars := 0
	if sys, ok := reqMap["system"].(string); ok {
		chars += len(sys)
	}
	if list, ok := reqMap["messages"].([]interface{}); ok {
		msgs = len(list)
		for _, m := range list {
			mMap, ok := m.(map[string]interface{})
			if !ok {
				continue
			}
			role, _ := mMap["role"].(string)
			if s, ok := mMap["content"].(string); ok {
				chars += len(s)
				if role == "system" && sysPrompt == "" {
					sysPrompt = runeSafeTruncate(s, 120)
				}
				if role == "user" {
					intent = runeSafePrefix(s, 80)
					if len(s) > len(intent) {
						intent += "…"
					}
				}
			} else if arr, ok := mMap["content"].([]interface{}); ok {
				for _, b := range arr {
					if bMap, ok := b.(map[string]interface{}); ok {
						if t, ok := bMap["text"].(string); ok {
							chars += len(t)
						}
					}
				}
			}
		}
	}
	if sys, ok := reqMap["system"].(string); ok && sysPrompt == "" {
		sysPrompt = runeSafeTruncate(sys, 120)
	}
	if tools, ok := reqMap["tools"].([]interface{}); ok {
		for _, t := range tools {
			tMap, ok := t.(map[string]interface{})
			if !ok {
				continue
			}
			if fn, ok := tMap["function"].(map[string]interface{}); ok {
				if s, ok := fn["name"].(string); ok {
					chars += len(s)
				}
				if s, ok := fn["description"].(string); ok {
					chars += len(s)
				}
			}
			if s, ok := tMap["name"].(string); ok {
				chars += len(s)
			}
			if s, ok := tMap["description"].(string); ok {
				chars += len(s)
			}
		}
	}
	if chars == 0 {
		chars = len(body)
	}
	est = int64(float64(chars) / 3.7)
	if est < 1 {
		est = 1
	}
	return msgs, sysPrompt, intent, est
}

func TestSummarizeWireRequestMatchesLegacyWalk(t *testing.T) {
	bodies := []string{
		`{"model":"gpt-4o","messages":[{"role":"system","content":"You are helpful"},{"role":"user","content":"fix the bug in main.go please"}]}`,
		`{"system":"anthropic sys","messages":[{"role":"user","content":[{"type":"text","text":"hello"},{"type":"tool_result","content":"big file"},{"type":"image","source":{}}]}],"tools":[{"name":"read_file","description":"reads","input_schema":{"type":"object"}}]}`,
		`{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":null},null,{"role":7,"content":"x"},{"role":"user","content":"` + string(make([]byte, 0)) + `ünïcödé ünïcödé ünïcödé ünïcödé ünïcödé ünïcödé ünïcödé ünïcödé ünïcödé ünïcödé ünïcödé ünïcödé"}]}`,
		`{"messages":"not-an-array","system":["block"],"tools":[{"type":"function","function":{"name":"edit","description":"edits files"}},5]}`,
		`{"messages":[{"role":"user","content":[{"text":5},{"text":"ok"}]}]}`,
		`[1,2,3]`,
		`{}`,
		`not json at all`,
		`{"messages":[{"role":"system","content":[{"text":"array sys"}]},{"role":"system","content":"string sys"}]}`,
	}
	for _, b := range bodies {
		body := []byte(b)
		wantN, wantSys, wantIntent, wantEst := legacyRequestSummary(body)
		got := summarizeWireRequest(body)
		gotEst := got.estimatedTokens(len(body))
		if got.messageCount != wantN || got.systemPrompt != wantSys || got.userIntent != wantIntent || gotEst != wantEst {
			t.Errorf("body %s\n got  n=%d sys=%q intent=%q est=%d\n want n=%d sys=%q intent=%q est=%d",
				b, got.messageCount, got.systemPrompt, got.userIntent, gotEst, wantN, wantSys, wantIntent, wantEst)
		}
		if e := EstimatePromptTokens(body); e != wantEst {
			t.Errorf("EstimatePromptTokens(%s) = %d, want %d", b, e, wantEst)
		}
	}
}
