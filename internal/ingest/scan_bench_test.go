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
