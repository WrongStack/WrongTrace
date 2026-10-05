package ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestTranscriptKeepsDistinctModelVariantIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.jsonl")
	for _, base := range []string{"gpt-4o", "gpt-4o-mini", "deepseek-v3", "claude-3-7-sonnet"} {
		for _, model := range []string{base, base + "-extra-fixture"} {
			for _, annotation := range []string{"", " (Preview)"} {
				body := fmt.Sprintf(`{"model":%q,"tool_calls":[{"name":"edit_file","args":{"file_path":"a.go"}}],"created_at":"2026-01-01T00:00:00Z"}`+"\n", model+annotation)
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
				events, err := ParseJSONLTranscript(path)
				if err != nil || len(events) != 1 || events[0].ModelName != model {
					t.Fatalf("model identity erased: input=%s events=%+v err=%v", model+annotation, events, err)
				}
			}
		}
	}
}
