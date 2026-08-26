package ingest

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// buildSessionTree fakes the shape of ~/.claude/projects: many per-workspace
// folders, each holding many transcripts.
func buildSessionTree(tb testing.TB, dirs, filesPerDir int) string {
	tb.Helper()
	root := tb.TempDir()
	row := []byte(`{"type":"PLANNER_RESPONSE","model":"gpt-4o","tool_calls":[{"name":"write_to_file","args":{"TargetFile":"main.go"}}],"usage":{"input_tokens":10,"output_tokens":1}}` + "\n")
	for d := 0; d < dirs; d++ {
		dir := filepath.Join(root, "slug-"+strconv.Itoa(d))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			tb.Fatal(err)
		}
		for f := 0; f < filesPerDir; f++ {
			if err := os.WriteFile(filepath.Join(dir, "s"+strconv.Itoa(f)+".jsonl"), row, 0o644); err != nil {
				tb.Fatal(err)
			}
		}
	}
	return root
}

// BenchmarkPollOnce_SteadyState measures the cost the daemon actually pays
// every tick: nothing changed, and the poll must prove it.
func BenchmarkPollOnce_SteadyState(b *testing.B) {
	root := buildSessionTree(b, 40, 100)
	sw := NewSessionWatcher(func(ToolCallEvent) {})
	sw.AddWatchDir(root)
	sw.PollOnce() // warm cursors and the directory cache

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sw.PollOnce()
	}
}

// buildWrongStackTree fakes the ~/.wrongstack/projects shape that motivated
// raising the scan depth: each project nests subagent transcripts at
// sessions/<date>/<session>/subagents/<date>/<session>/transcript.jsonl,
// six directory levels below the watched root.
func buildWrongStackTree(tb testing.TB, projects int) string {
	tb.Helper()
	root := tb.TempDir()
	row := []byte(`{"type":"PLANNER_RESPONSE","model":"gpt-4o","tool_calls":[{"name":"write_to_file","args":{"TargetFile":"main.go"}}],"usage":{"input_tokens":10,"output_tokens":1}}` + "\n")
	for p := 0; p < projects; p++ {
		id := strconv.Itoa(p)
		sessionDir := filepath.Join(root, "proj-"+id, "sessions", "2026-08-26", "sess-"+id)
		subagentDir := filepath.Join(sessionDir, "subagents", "2026-08-26", "sub-sess-"+id)
		if err := os.MkdirAll(subagentDir, 0o755); err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sessionDir, "transcript.jsonl"), row, 0o644); err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(subagentDir, "sub_transcript.jsonl"), row, 0o644); err != nil {
			tb.Fatal(err)
		}
	}
	return root
}

// BenchmarkPollOnce_DeepSubagentTree measures the worst case of the deeper
// default bound: every level-six and level-seven directory holds recently
// touched transcripts, so tiering grants no skip. Steady state here is an
// upper bound on what extending depth from five to eight can add.
func BenchmarkPollOnce_DeepSubagentTree(b *testing.B) {
	root := buildWrongStackTree(b, 40)
	sw := NewSessionWatcher(func(ToolCallEvent) {})
	sw.AddWatchDir(root)
	sw.PollOnce() // warm cursors

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sw.PollOnce()
	}
}

// BenchmarkWalkDirBaseline is the floor the previous implementation could not
// go below: a bare filepath.WalkDir over the same tree that does nothing but
// count matches. A steady-state poll should now cost less than this, because
// it skips dormant directories entirely rather than re-enumerating them.
func BenchmarkWalkDirBaseline(b *testing.B) {
	root := buildSessionTree(b, 40, 100)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		seen := 0
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d == nil || d.IsDir() {
				return nil
			}
			if filepath.Ext(path) == ".jsonl" {
				if info, iErr := d.Info(); iErr == nil && info != nil {
					seen++
				}
			}
			return nil
		})
		if seen == 0 {
			b.Fatal("baseline walk found nothing")
		}
	}
}
