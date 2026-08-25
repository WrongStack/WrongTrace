package ast

import (
	"os"
	"strings"
	"testing"
)

// Benchmarks for the two CPU-hot AST operations. They parse this package's
// own real sources so the corpus is representative Go (mix of funcs, methods,
// structs, ~800 lines) without needing fixtures:
//
//	go test ./internal/ast -bench 'BenchmarkParse|BenchmarkDiff' -benchmem -count=6
//
// Run with -count and pipe through benchstat when comparing before/after any
// parser or diff change.

// benchEngine returns a shared engine (parsers are stateless; reusing one
// keeps NewEngine cost out of every iteration).
func benchEngine(b *testing.B) *Engine {
	b.Helper()
	eng, err := NewEngine()
	if err != nil {
		b.Fatalf("NewEngine: %v", err)
	}
	return eng
}

func benchSource(b *testing.B, name string) []byte {
	b.Helper()
	src, err := os.ReadFile(name)
	if err != nil {
		b.Fatalf("read %s: %v", name, err)
	}
	return src
}

// BenchmarkParse measures a full cold parse: tree-sitter parse + walk +
// signature build + body hashing. This is the cost PrimeDirectory pays per
// file and HandleFileChange pays whenever the content hash changes.
func BenchmarkParse(b *testing.B) {
	eng := benchEngine(b)
	src := benchSource(b, "parser.go")
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		snap, err := eng.Parse("parser.go", src)
		if err != nil || snap == nil {
			b.Fatalf("parse: %v", err)
		}
	}
}

// benchDiffPair builds prev/next snapshots of diff.go with a synthetic
// mid-file insertion — the common agent-edit shape (small mid-region after
// common prefix/suffix trimming, exercises the LCS DP).
func benchDiffPair(b *testing.B) (*FileSnapshot, *FileSnapshot) {
	b.Helper()
	eng := benchEngine(b)
	src := benchSource(b, "diff.go")
	half := len(src) / 2
	mutated := make([]byte, 0, len(src)+64)
	mutated = append(mutated, src[:half]...)
	mutated = append(mutated, []byte("\nfunc __benchTmp() string { return \"x\" }\n")...)
	mutated = append(mutated, src[half:]...)
	prev, err := eng.Parse("diff.go", src)
	if err != nil {
		b.Fatalf("parse prev: %v", err)
	}
	next, err := eng.Parse("diff.go", mutated)
	if err != nil {
		b.Fatalf("parse next: %v", err)
	}
	return prev, next
}

// BenchmarkDiffMidEdit is the steady-state watcher path: file saved with a
// small edit, prev snapshot cached.
func BenchmarkDiffMidEdit(b *testing.B) {
	prev, next := benchDiffPair(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res := Diff("bench", prev, next)
		if len(res.Events) == 0 {
			b.Fatal("expected at least one event")
		}
	}
}

// BenchmarkDiffNewFile measures the prev==nil path (first observation of a
// file): all ADDED events with full-body snippets.
func BenchmarkDiffNewFile(b *testing.B) {
	eng := benchEngine(b)
	snap, err := eng.Parse("diff.go", benchSource(b, "diff.go"))
	if err != nil {
		b.Fatalf("parse: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Diff("bench", nil, snap)
	}
}

// BenchmarkDiffReformatFallback measures the pathological case behind the
// m*n > 200000 guard in generateLineDiff: whole-file reformat where most
// lines differ. The mutation (tab re-indent + two trailing lines) keeps the
// trimmed mid-region ABOVE the guard so the degraded emit-everything fallback
// path is what gets measured — a one-space reindent lands just under 200k
// cells and would silently benchmark the full DP instead.
func BenchmarkDiffReformatFallback(b *testing.B) {
	eng := benchEngine(b)
	src := benchSource(b, "diff.go")
	prev, err := eng.Parse("diff.go", src)
	if err != nil {
		b.Fatalf("parse prev: %v", err)
	}
	lines := strings.Split(string(src), "\n")
	for i := range lines {
		lines[i] = "\t" + lines[i]
	}
	lines = append(lines, "// reformatted", "// reformatted")
	mutated := []byte(strings.Join(lines, "\n"))
	next, err := eng.Parse("diff.go", mutated)
	if err != nil {
		b.Fatalf("parse next: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Diff("bench", prev, next)
	}
}
