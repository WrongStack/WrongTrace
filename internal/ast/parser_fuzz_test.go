package ast

// FuzzEngineParse fuzzes the AST parse path — the parser consuming arbitrary
// source bytes for every watched file (tree-sitter interop behind parseMu,
// the hand-rolled braceScanner lexer, signature extraction, and
// DetectLanguage's extension matching).
//
// Contract: Parse never panics on arbitrary (path, src) — it returns a
// snapshot or an error. The engine is memoized per test process via sync.Once
// (NewEngine is expensive), which matches the production serialized shape.
//
// History: promoted 2026-09-09 from a temporary round-8 probe after a clean
// 60s campaign (18,956,526 execs, zero failures). The same-named target
// recovers that campaign's ~513-input corpus from GOCACHE, so every
// `-fuzz=FuzzEngineParse` run keeps accruing coverage; plain `go test` runs
// only the seeds below (microseconds). The seeds carry the shapes that
// historically broke braceScanner: braces inside strings and block comments,
// template literals, unterminated tails, escaped quotes, CRLF, unicode, and
// unknown extensions.

import (
	"sync"
	"testing"
)

var (
	fuzzEngineOnce sync.Once
	fuzzEngine     *Engine
)

func fuzzEngineGet(t *testing.T) *Engine {
	fuzzEngineOnce.Do(func() {
		e, err := NewEngine()
		if err != nil {
			t.Fatalf("NewEngine: %v", err)
		}
		fuzzEngine = e
	})
	return fuzzEngine
}

func FuzzEngineParse(f *testing.F) {
	f.Add("main.go", "package main\nfunc Test() {}")
	f.Add("app.ts", "const a = { b: '}' };\nfunction f() { return `tpl ${x} }`; }")
	f.Add("x.py", "def f():\n    s = 'unterminated")
	f.Add("y.c", "/* block comment with } inside\nint main() { return 0; }")
	f.Add("z.generic", "weird { { { unterminated")
	f.Add("w.rs", "fn main() { let s = \"\\\" }}\"; }")
	f.Add("a.jsonl", "{\"not\":\"code\"}")
	f.Add("", "")
	f.Add("u.go", "package p // ünïcödé 🎉\nfunc F() { /* CR\r\n } */ }")

	f.Fuzz(func(t *testing.T, path, src string) {
		e := fuzzEngineGet(t)
		_, _ = e.Parse(path, []byte(src))
	})
}
