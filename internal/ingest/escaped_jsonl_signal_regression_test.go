package ingest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestJSONLSignalsAcceptUnicodeEscapes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.jsonl")
	plain := `{"tool_calls":[{"name":"read_file","args":{"file_path":"a.go"}}],"created_at":"2026-01-01T00:00:00Z"}` + "\n"
	for _, body := range []string{
		`{"tool\u005fcalls":[{"name":"read_file","args":{"file_path":"a.go"}}],"created_at":"2026-01-01T00:00:00Z"}` + "\n",
		`{"type":"USER\u005fINPUT","content":"fixture intent"}` + "\n" + plain,
		`{"mo\u0064el":"fixture-model"}` + "\n" + plain,
	} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		_, reads, err := ParseJSONLTranscriptFull(path)
		if err != nil || len(reads) != 1 {
			t.Fatalf("escaped signal skipped: count%d err%v", len(reads), err)
		}
		if body[2:6] == "type" && reads[0].Intent != "fixture intent" {
			t.Fatal("escaped input intent lost")
		}
		if body[2:4] == "mo" && reads[0].ModelName != "fixture-model" {
			t.Fatal("escaped model lost")
		}
	}
}
