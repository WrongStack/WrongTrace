package ingest

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/wrongstack/wrongtrace/internal/models"
)

func TestJSONLUsagePreservesCacheAndBilledOutput(t *testing.T) {
	for _, tc := range []struct {
		name, model, usage     string
		prompt, output, cached int64
	}{
		{"uncached-control", "gpt-4o", `{"prompt_tokens":1000,"completion_tokens":200}`, 1000, 200, 0},
		{"openai-cache", "gpt-4o", `{"prompt_tokens":1000,"completion_tokens":200,"prompt_tokens_details":{"cached_tokens":800}}`, 1000, 200, 800},
		{"anthropic-cache", "claude-sonnet-4-5", `{"input_tokens":1000,"cache_read_input_tokens":500,"cache_creation_input_tokens":200,"output_tokens":100}`, 1700, 100, 500},
		{"responses-inclusive-cache", "gpt-4o", `{"input_tokens":1000,"output_tokens":200,"input_tokens_details":{"cached_tokens":800}}`, 1000, 200, 800},
		{"gemini-cache-and-thinking", "gemini-2.5-pro", `{"promptTokenCount":1000,"candidatesTokenCount":50,"thoughtsTokenCount":20,"cachedContentTokenCount":700}`, 1000, 70, 700},
		{"thinking-only", "gemini-2.5-pro", `{"promptTokenCount":1000,"thoughtsTokenCount":20}`, 1000, 20, 0},
		{"normalized-output-keeps-total", "gemini-2.5-pro", `{"prompt_tokens":1000,"completion_tokens":70,"candidatesTokenCount":50,"thoughtsTokenCount":20}`, 1000, 70, 0},
		{"normalized-camel-output-keeps-total", "gemini-2.5-pro", `{"inputTokens":1000,"outputTokens":70,"thoughtsTokenCount":20}`, 1000, 70, 0},
		{"zero-cache-control", "gpt-4o", `{"prompt_tokens":1000,"completion_tokens":0,"prompt_tokens_details":{"cached_tokens":0}}`, 1000, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			writeFile(t, path, fmt.Sprintf(`{"model":%q,"usage":%s,"tool_calls":[{"name":"edit_file","args":{"path":"subject.go"}},{"name":"read_file","args":{"path":"subject.go"}}]}`+"\n", tc.model, tc.usage))
			mods, reads, err := ParseJSONLTranscriptFull(path)
			if err != nil || len(mods) != 1 || len(reads) != 1 {
				t.Fatalf("fixture setup: mods=%+v reads=%+v err=%v", mods, reads, err)
			}
			wantCost, _ := models.Global.CalculateCostDetailed("", tc.model, tc.prompt, tc.output, tc.cached)
			t.Logf("EXPECTED: prompt/output/cache=%d/%d/%d, cost=%.8f | ACTUAL: %d/%d/%d, cost=%.8f", tc.prompt, tc.output, tc.cached, wantCost, mods[0].PromptTokens, mods[0].CompletionTokens, reads[0].CachedTokens, mods[0].CostUSD)
			if mods[0].PromptTokens != tc.prompt || mods[0].CompletionTokens != tc.output || reads[0].PromptTokens != tc.prompt || reads[0].CachedTokens != tc.cached || math.Abs(mods[0].CostUSD-wantCost) > 1e-12 || math.Abs(reads[0].CostUSD-wantCost) > 1e-12 {
				t.Error("cached/billed usage was omitted or charged as uncached input")
			}
		})
	}
}

func TestJSONLCachedUsageRebuildsLegacyCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	checkpoint := filepath.Join(t.TempDir(), "offsets.json")
	cached := `{"model":"gpt-4o","usage":{"prompt_tokens":1000,"completion_tokens":100,"prompt_tokens_details":{"cached_tokens":800}},"tool_calls":[{"name":"edit_file","args":{"path":"first.go"}}]}` + "\n"
	uncached := `{"usage":{"prompt_tokens":200,"completion_tokens":50},"tool_calls":[{"name":"edit_file","args":{"path":"second.go"}}]}` + "\n"
	writeFile(t, path, cached+uncached)
	// Shape written before cache accounting: totals exist, but include the
	// old undiscounted cost. Resuming must rebuild it without replaying edits.
	data, err := json.Marshal(offsetCheckpoint{
		Version:  1,
		Offsets:  map[string]int64{path: int64(len(cached))},
		Contexts: map[string]jsonlContext{path: {ModelName: "gpt-4o", Totals: true, PromptTokens: 1000, CompletionTokens: 100, CostUSD: models.Global.CalculateCost("gpt-4o", 1000, 100)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(checkpoint, data, 0600); err != nil {
		t.Fatal(err)
	}
	var got []ToolCallEvent
	watcher := NewSessionWatcher(func(ev ToolCallEvent) { got = append(got, ev) })
	if err := watcher.EnablePersistentOffsets(checkpoint); err != nil {
		t.Fatal(err)
	}
	watcher.AddWatchDir(filepath.Dir(path))
	watcher.PollOnce()
	wantFirst, _ := models.Global.CalculateCostDetailed("", "gpt-4o", 1000, 100, 800)
	wantSecond, _ := models.Global.CalculateCostDetailed("", "gpt-4o", 200, 50, 0)
	if len(got) != 1 || got[0].TargetFile != "second.go" || math.Abs(got[0].CostUSD-wantFirst-wantSecond) > 1e-12 {
		t.Fatalf("EXPECTED: only second.go with rebuilt cost=%.8f | ACTUAL: %+v", wantFirst+wantSecond, got)
	}
}

func TestJSONLCachedUsageSurvivesCheckpointResume(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	checkpoint := filepath.Join(t.TempDir(), "offsets.json")
	cached := `{"model":"gpt-4o","usage":{"prompt_tokens":1000,"completion_tokens":100,"prompt_tokens_details":{"cached_tokens":800}},"tool_calls":[{"name":"edit_file","args":{"path":"first.go"}}]}` + "\n"
	uncached := `{"usage":{"prompt_tokens":200,"completion_tokens":50},"tool_calls":[{"name":"edit_file","args":{"path":"second.go"}}]}` + "\n"
	writeFile(t, path, cached)
	first := NewSessionWatcher(nil)
	if err := first.EnablePersistentOffsets(checkpoint); err != nil {
		t.Fatal(err)
	}
	first.AddWatchDir(filepath.Dir(path))
	first.PollOnce()
	writeFile(t, path, cached+uncached)
	var got []ToolCallEvent
	second := NewSessionWatcher(func(ev ToolCallEvent) { got = append(got, ev) })
	if err := second.EnablePersistentOffsets(checkpoint); err != nil {
		t.Fatal(err)
	}
	second.AddWatchDir(filepath.Dir(path))
	second.PollOnce()
	if len(got) != 1 {
		t.Fatalf("resumed events=%+v", got)
	}
	firstCost, _ := models.Global.CalculateCostDetailed("", "gpt-4o", 1000, 100, 800)
	lastCost, _ := models.Global.CalculateCostDetailed("", "gpt-4o", 200, 50, 0)
	if got[0].PromptTokens != 1200 || got[0].CompletionTokens != 150 || math.Abs(got[0].CostUSD-firstCost-lastCost) > 1e-12 {
		t.Fatalf("EXPECTED: cumulative cost=%.8f, 1200/150 | ACTUAL: %+v", firstCost+lastCost, got[0])
	}
}
