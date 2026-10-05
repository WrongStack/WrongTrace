package ingest

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/wrongstack/wrongtrace/internal/db"
)

func TestTranscriptIdentitySeparatesFullPaths(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) string {
		t.Helper()
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	line := `{"model":"fixture-model","tool_calls":[{"name":"read_file","args":{"file_path":"a.go"}}],"created_at":"2026-01-01T00:00:00Z"}` + "\n"
	aPath := write("one/session/transcript.jsonl", line)
	bPath := write("two/session/transcript.jsonl", line)
	_, a, err := ParseJSONLTranscriptFull(aPath)
	if err != nil {
		t.Fatal(err)
	}
	_, b, err := ParseJSONLTranscriptFull(bPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 1 || len(b) != 1 || a[0].SessionID == b[0].SessionID || a[0].ReadID == b[0].ReadID {
		t.Fatal("independent transcripts collided")
	}
	store, err := db.Open(filepath.Join(dir, "reads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Migrate(); err != nil {
		t.Fatal(err)
	}
	for _, ev := range []FileReadEvent{a[0], b[0]} {
		if err = store.InsertReadEvent(db.FileReadRecord{ReadID: ev.ReadID, RepoName: "fixture", FilePath: ev.FilePath, ReadTime: ev.OccurredAt}); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err = store.DB().QueryRow("SELECT COUNT(*) FROM file_read_events").Scan(&count); err != nil || count != 2 {
		t.Fatalf("independent read lost: count%d err%v", count, err)
	}
	if sessionIDForPath(aPath) != sessionIDForPath(filepath.Join(filepath.Dir(aPath), ".", filepath.Base(aPath))) {
		t.Fatal("lexical path alias changed ID")
	}
	if runtime.GOOS == "windows" && sessionIDForPath(aPath) != sessionIDForPath(strings.ToUpper(aPath)) {
		t.Fatal("Windows case alias changed ID")
	}
	cline := `{"model":"fixture-model","messages":[{"say":"tool","text":"a.go","ts":1700000000000}]}`
	c, err := ParseClineTask(write("cline/one/api_conversation_history.json", cline))
	if err != nil {
		t.Fatal(err)
	}
	d, err := ParseClineTask(write("cline/two/api_conversation_history.json", cline))
	if err != nil {
		t.Fatal(err)
	}
	if len(c) != 1 || len(d) != 1 || c[0].SessionID == d[0].SessionID {
		t.Fatal("Cline task owners collided")
	}
	x, err := ParseAiderHistory(write("checkout-one/api/.aider.chat.history.md", "Model: fixture-model\nApplied edit to a.go\n"))
	if err != nil {
		t.Fatal(err)
	}
	y, err := ParseAiderHistory(write("checkout-two/api/.aider.chat.history.md", "Model: fixture-model\nApplied edit to a.go\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(x) != 1 || len(y) != 1 || x[0].SessionID == y[0].SessionID {
		t.Fatal("same-named Aider checkout owners collided")
	}
}
