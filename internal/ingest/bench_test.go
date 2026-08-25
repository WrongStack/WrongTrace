package ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Benchmarks for SessionWatcher.PollOnce — the 25-second background walk over
// agent transcript trees (cmd/wrongtrace/main.go:319). Two shapes matter:
//
//   - Cold:  first poll over an unseen tree (walk + full JSONL parse).
//   - Steady: every subsequent poll with no new data (walk + stat + skip).
//     This is the idle CPU floor the daemon pays forever; it is the number
//     any polling-frequency or fsnotify optimization must move.
//
//	go test ./internal/ingest -bench BenchmarkPollOnce -benchmem -count=6

// benchTranscriptTree builds a deterministic agent-log tree under t.TempDir():
// numSessions session dirs each holding one transcript.jsonl of linesPerFile
// lines (2/5 tool-call rows, 3/5 noise rows that fail the fast pre-filter),
// plus a node_modules dir of junk that must be pruned by isIgnoredLogDir.
func benchTranscriptTree(b *testing.B, numSessions, linesPerFile int) string {
	b.Helper()
	root := b.TempDir()
	for s := 0; s < numSessions; s++ {
		dir := filepath.Join(root, fmt.Sprintf("session-%02d", s))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatalf("mkdir: %v", err)
		}
		var content []byte
		for i := 0; i < linesPerFile; i++ {
			switch i % 5 {
			case 0, 1: // file-modifying tool calls (pass the pre-filter)
				content = append(content, []byte(fmt.Sprintf(
					`{"type":"assistant","tool_calls":[{"name":"Write","args":{"file_path":"pkg%d/file%d.go"}}],"usage":{"input_tokens":100,"output_tokens":20}}`+"\n", s, i))...)
			case 2: // model metadata row (passes the pre-filter, no tool call)
				content = append(content, []byte(fmt.Sprintf(
					`{"type":"status","model":"claude-opus-4-%d","usage":{"input_tokens":10,"output_tokens":1}}`+"\n", s))...)
			default: // noise: fails the `"tool`/"model"/"usage" substring gate
				content = append(content, []byte(fmt.Sprintf(
					`{"type":"progress","message":"thinking hard %d"}`+"\n", i))...)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "transcript.jsonl"), content, 0o644); err != nil {
			b.Fatalf("write transcript: %v", err)
		}
	}
	junk := filepath.Join(root, "node_modules", "electron", "dist")
	if err := os.MkdirAll(junk, 0o755); err != nil {
		b.Fatalf("mkdir junk: %v", err)
	}
	if err := os.WriteFile(filepath.Join(junk, "noise.jsonl"), []byte("{\"tool_calls\":[]}\n"), 0o644); err != nil {
		b.Fatalf("write junk: %v", err)
	}
	return root
}

// BenchmarkPollOnceCold measures the first poll over an unseen tree: full
// directory walk plus parsing every transcript line. A fresh watcher is built
// per iteration, so parse cost is paid every time.
func BenchmarkPollOnceCold(b *testing.B) {
	root := benchTranscriptTree(b, 20, 200)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sw := NewSessionWatcher(nil)
		sw.AddWatchDir(root)
		sw.PollOnce()
	}
}

// BenchmarkPollOnceSteadyIdle measures the steady-state poll over an already
// fully-ingested tree with zero new bytes: walk + DirEntry stat + offset
// comparison + skip. This is the recurring idle cost of the 25s ticker.
func BenchmarkPollOnceSteadyIdle(b *testing.B) {
	root := benchTranscriptTree(b, 20, 200)
	sw := NewSessionWatcher(nil)
	sw.AddWatchDir(root)
	sw.PollOnce() // warm: records sizes/offsets for every file
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sw.PollOnce()
	}
}
