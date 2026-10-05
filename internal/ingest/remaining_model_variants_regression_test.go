package ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTranscriptKeepsRemainingDisplayModelVariants(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.jsonl")
	for _, family := range []struct{ display, base string }{
		{"Gemini 3.7 Flash", "gemini-3.7-flash"},
		{"Gemini 2.5 Pro", "gemini-2.5-pro"},
		{"Gemini 2.0 Flash", "gemini-2.0-flash"},
		{"Gemini 1.5 Pro", "gemini-1.5-pro"},
		{"Claude 3.5 Sonnet", "claude-3-5-sonnet"},
		{"DeepSeek R1", "deepseek-r1"},
		{"o3-mini", "o3-mini"},
	} {
		variantDisplay := family.display + " extra-fixture"
		displayID := strings.ReplaceAll(strings.ToLower(variantDisplay), " ", "-")
		variantID := family.base + "-extra-fixture"
		for _, tc := range []struct{ input, want string }{
			{family.display, family.base}, {family.base, family.base},
			{variantDisplay, displayID}, {variantID, variantID},
			{strings.ToUpper(variantDisplay) + " (Preview)", displayID},
			{family.display + " (Default)", family.base},
		} {
			body := fmt.Sprintf(`{"model":%q,"tool_calls":[{"name":"edit_file","args":{"file_path":"a.go"}}],"created_at":"2026-01-01T00:00:00Z"}`+"\n", tc.input)
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			events, err := ParseJSONLTranscript(path)
			if err != nil || len(events) != 1 || events[0].ModelName != tc.want {
				t.Fatalf("input%q want%s events%+v err%v", tc.input, tc.want, events, err)
			}
		}
	}
}
