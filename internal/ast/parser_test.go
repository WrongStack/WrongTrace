package ast

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	eng, err := NewEngine()
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	t.Cleanup(eng.Close)
	return eng
}

func parseOrFatal(t *testing.T, eng *Engine, path, src string) *FileSnapshot {
	t.Helper()
	snap, err := eng.Parse(path, []byte(src))
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if snap == nil {
		t.Fatalf("parse %s: nil snapshot for supported language", path)
	}
	return snap
}

// findEvent returns the event whose signature contains sig and whose action
// matches, or nil.
func findEvent(events []Event, action Action, sig string) *Event {
	for i := range events {
		if events[i].Action == action && strings.Contains(events[i].Signature, sig) {
			return &events[i]
		}
	}
	return nil
}

// TestDiffTransitions_Go exercises all three lifecycle transitions in one
// diff: Beta removed (DELETED), Alpha body changed (MODIFIED), Gamma new
// (ADDED), and asserts the stable DELETED→MODIFIED→ADDED ordering.
func TestDiffTransitions_Go(t *testing.T) {
	eng := newTestEngine(t)

	v1 := "package main\n\nfunc Alpha() int { return 1 }\n\nfunc Beta() int { return 2 }\n"
	v2 := "package main\n\nfunc Alpha() int { return 42 }\n\nfunc Gamma() int { return 3 }\n"

	prev := parseOrFatal(t, eng, "main.go", v1)
	next := parseOrFatal(t, eng, "main.go", v2)

	res := Diff("repo", prev, next)
	if len(res.Events) != 3 {
		t.Fatalf("want 3 events, got %d: %+v", len(res.Events), res.Events)
	}

	// Ordering: DELETED, then MODIFIED, then ADDED.
	ranks := make([]int, len(res.Events))
	for i, ev := range res.Events {
		ranks[i] = actionRank(ev.Action)
	}
	for i := 1; i < len(ranks); i++ {
		if ranks[i] < ranks[i-1] {
			t.Errorf("events not ordered DELETED→MODIFIED→ADDED: %+v", res.Events)
		}
	}

	del := findEvent(res.Events, ActionDeleted, "Beta")
	if del == nil {
		t.Fatalf("missing DELETED for Beta: %+v", res.Events)
	}
	if del.NodeType != NodeFunction || del.FilePath != "main.go" || del.RepoName != "repo" {
		t.Errorf("DELETED Beta metadata wrong: %+v", del)
	}

	mod := findEvent(res.Events, ActionModified, "Alpha")
	if mod == nil {
		t.Fatalf("missing MODIFIED for Alpha: %+v", res.Events)
	}
	if mod.BodyHash == "" {
		t.Error("MODIFIED Alpha has empty body hash")
	}
	if old := prev.Nodes[mod.Signature]; old.Hash == mod.BodyHash {
		t.Error("MODIFIED Alpha hash equals previous hash; body change not detected")
	}

	add := findEvent(res.Events, ActionAdded, "Gamma")
	if add == nil {
		t.Fatalf("missing ADDED for Gamma: %+v", res.Events)
	}
	if add.LOC < 1 {
		t.Errorf("ADDED Gamma LOC: want >= 1, got %d", add.LOC)
	}
	if res.NewSnap == nil || res.NewSnap.Path != "main.go" {
		t.Error("DiffResult.NewSnap not set to next snapshot")
	}
}

// TestDiffNilSnapshots covers the cold-start and file-removal edges: a nil
// previous snapshot yields only ADDED, a nil next snapshot only DELETED.
func TestDiffNilSnapshots(t *testing.T) {
	eng := newTestEngine(t)
	src := "package main\n\nfunc One() int { return 1 }\n\nfunc Two() int { return 2 }\n"

	cold := Diff("repo", nil, parseOrFatal(t, eng, "m.go", src))
	if len(cold.Events) != 2 {
		t.Fatalf("cold start: want 2 ADDED, got %d: %+v", len(cold.Events), cold.Events)
	}
	for _, ev := range cold.Events {
		if ev.Action != ActionAdded {
			t.Errorf("cold start: want ADDED, got %s for %s", ev.Action, ev.Signature)
		}
	}

	gone := Diff("repo", parseOrFatal(t, eng, "m.go", src), nil)
	if len(gone.Events) != 2 {
		t.Fatalf("file removed: want 2 DELETED, got %d: %+v", len(gone.Events), gone.Events)
	}
	for _, ev := range gone.Events {
		if ev.Action != ActionDeleted {
			t.Errorf("file removed: want DELETED, got %s for %s", ev.Action, ev.Signature)
		}
	}
}

// TestDiff_NoChangeWhenOnlyCosmetic asserts the core noise-suppression
// contract: whitespace churn and comment add/remove must NOT emit MODIFIED
// because the normalized body hash is unchanged.
func TestDiff_NoChangeWhenOnlyCosmetic(t *testing.T) {
	eng := newTestEngine(t)

	base := "package main\n\nfunc Alpha() int { return 1 }\n"
	// Valid Go throughout: only widen existing whitespace runs (the brace
	// must stay on the signature line or the Go grammar errors out).
	spaced := "package main\n\nfunc  Alpha()  int   {   return   1 }\n"
	commented := "package main\n\n// rewritten by agent\nfunc Alpha() int { return 1 /* inline */ }\n"

	hBase := parseOrFatal(t, eng, "m.go", base).Nodes["function:m.go::Alpha"].Hash
	hSpaced := parseOrFatal(t, eng, "m.go", spaced).Nodes["function:m.go::Alpha"].Hash
	hCommented := parseOrFatal(t, eng, "m.go", commented).Nodes["function:m.go::Alpha"].Hash

	if hBase == "" {
		t.Fatal("base Alpha hash empty; signature key mismatch")
	}
	if hBase != hSpaced {
		t.Error("whitespace-only change altered the normalized hash")
	}
	if hBase != hCommented {
		t.Error("comment-only change altered the normalized hash")
	}

	res := Diff("repo", parseOrFatal(t, eng, "m.go", base), parseOrFatal(t, eng, "m.go", commented))
	if len(res.Events) != 0 {
		t.Errorf("cosmetic-only edit emitted %d events, want 0: %+v", len(res.Events), res.Events)
	}
}

// TestDiff_StringLiteralChangeIsSemantic asserts the normalizer does not
// swallow string contents: changing a literal must flip the hash and emit
// MODIFIED. Also pins that '//' inside a string is data, not a comment.
func TestDiff_StringLiteralChangeIsSemantic(t *testing.T) {
	eng := newTestEngine(t)

	v1 := "package main\n\nfunc Msg() string { return \"v1\" }\n"
	v2 := "package main\n\nfunc Msg() string { return \"v2\" }\n"

	prev := parseOrFatal(t, eng, "m.go", v1)
	next := parseOrFatal(t, eng, "m.go", v2)

	if prev.Nodes["function:m.go::Msg"].Hash == next.Nodes["function:m.go::Msg"].Hash {
		t.Fatal("string literal change produced identical hashes")
	}
	res := Diff("repo", prev, next)
	if ev := findEvent(res.Events, ActionModified, "Msg"); ev == nil {
		t.Errorf("string literal change did not emit MODIFIED: %+v", res.Events)
	}
}

// TestNormalizeForHash pins the normalizer contract directly: comments
// stripped, whitespace collapsed, string literals preserved verbatim
// (including comment-like content inside them).
func TestNormalizeForHash(t *testing.T) {
	cases := []struct {
		name string
		lang Language
		in   string
		want string
	}{
		{"line comment stripped", LangGo, "a // tail\nb", "a b"},
		{"python comment stripped", LangPython, "a # tail\nb", "a b"},
		{"python private-name marker preserved (JS not Python)", LangJavaScript, "class A { #x = 1 }", "class A { #x = 1 }"},
		{"python private-dunder preserved", LangPython, "self.__x = 1", "self.__x = 1"},
		{"block comment stripped", LangGo, "a /* mid */ b", "a b"},
		{"whitespace collapsed", LangGo, "a\n\t b   c", "a b c"},
		{"string kept verbatim", LangGo, `x := "a  b // c"`, `x := "a  b // c"`},
		{"python string kept verbatim", LangPython, `x := "a # not a comment"`, `x := "a # not a comment"`},
		{"raw string kept", LangGo, "s := `a /* b */`", "s := `a /* b */`"},
		{"empty", LangGo, "   \n\t ", ""},
	}
	for _, c := range cases {
		if got := normalizeForHash(c.in, c.lang); got != c.want {
			t.Errorf("%s: normalizeForHash(%q, %v) = %q, want %q", c.name, c.in, c.lang, got, c.want)
		}
	}

	// Equivalent inputs must hash identically (through the public path).
	if normalizeForHash("a // x\nb", LangGo) != normalizeForHash("a /* y */ b", LangGo) {
		t.Error("different comment styles with same code normalized differently")
	}
}

// TestParse_GoSignatures checks signature extraction for functions, methods
// (receiver-qualified), and struct type declarations.
func TestParse_GoSignatures(t *testing.T) {
	eng := newTestEngine(t)
	src := "package main\n\ntype Server struct {\n\tPort int\n}\n\nfunc (s *Server) Listen() error { return nil }\n\nfunc Free() {}\n"
	snap := parseOrFatal(t, eng, "srv.go", src)

	expect := map[string]NodeKind{}
	for sig, kind := range map[string]NodeKind{
		"function:srv.go::Free":    NodeFunction,
		"struct:srv.go::Server":    NodeStruct,
	} {
		expect[sig] = kind
	}
	for sig, want := range expect {
		n, ok := snap.Nodes[sig]
		if !ok {
			t.Errorf("missing signature %q; got %+v", sig, snap.SortedSignatures())
			continue
		}
		if n.Kind != want {
			t.Errorf("signature %q kind = %q, want %q", sig, n.Kind, want)
		}
	}

	// Method: receiver-qualified; exact receiver spelling is grammar-defined,
	// so assert the stable parts (kind prefix, receiver contains Server, name).
	var methodSig string
	for _, sig := range snap.SortedSignatures() {
		if strings.HasPrefix(sig, "method:") && strings.HasSuffix(sig, ".Listen") {
			methodSig = sig
			break
		}
	}
	if methodSig == "" {
		t.Fatalf("no method signature for Listen; got %+v", snap.SortedSignatures())
	}
	if !strings.Contains(methodSig, "Server") {
		t.Errorf("method signature %q lacks receiver type", methodSig)
	}
}

// TestParse_MultiLanguage runs the real Tree-sitter grammars for Python and
// TS/JS, asserting nodes are extracted in each language.
func TestParse_MultiLanguage(t *testing.T) {
	eng := newTestEngine(t)

	py := parseOrFatal(t, eng, "app.py", "def foo(x):\n    return x\n\nclass Bar:\n    def m(self):\n        pass\n")
	if _, ok := py.Nodes["function:app.py::foo"]; !ok {
		t.Errorf("python function missing; got %+v", py.SortedSignatures())
	}
	if _, ok := py.Nodes["class:app.py::Bar"]; !ok {
		t.Errorf("python class missing; got %+v", py.SortedSignatures())
	}

	ts := parseOrFatal(t, eng, "mod.ts", "export function bar() { return 1 }\n\nclass Qux {\n  m() { return 2 }\n}\nconst h = () => 3\n")
	if _, ok := ts.Nodes["function:mod.ts::bar"]; !ok {
		t.Errorf("ts function missing; got %+v", ts.SortedSignatures())
	}
	if _, ok := ts.Nodes["class:mod.ts::Qux"]; !ok {
		t.Errorf("ts class missing; got %+v", ts.SortedSignatures())
	}
	if _, ok := ts.Nodes["method:mod.ts::m"]; !ok {
		t.Errorf("ts method missing; got %+v", ts.SortedSignatures())
	}
	if _, ok := ts.Nodes["arrow_function:mod.ts::h"]; !ok {
		t.Errorf("named ts arrow function missing; got %+v", ts.SortedSignatures())
	}
}

// TestParse_Concurrent drives Parse + snapshot accessors from many
// goroutines at once, mirroring the real load shape: the watcher fires
// debounce callbacks on one goroutine per pending path. Under -race this
// guards the parser pool (a *sitter.Parser wraps a stateful C object and
// must never be driven concurrently) and the snapshot cache.
func TestParse_Concurrent(t *testing.T) {
	eng := newTestEngine(t)

	srcs := []struct {
		path string
		src  string
	}{
		{"a.go", "package main\n\nfunc A() int { return 1 }\n"},
		{"b.go", "package main\n\nfunc B() int { return 2 }\n"},
		{"c.py", "def c():\n    return 3\n"},
		{"d.js", "function d() { return 4 }\n"},
	}

	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers*len(srcs))
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for round := 0; round < 5; round++ {
				for i, s := range srcs {
					snap, err := eng.Parse(s.path, []byte(s.src))
					if err != nil {
						errs <- fmt.Errorf("w%d r%d parse %s: %w", w, round, s.path, err)
						continue
					}
					if snap == nil {
						errs <- fmt.Errorf("w%d r%d parse %s: nil snapshot", w, round, s.path)
						continue
					}
					if len(snap.Nodes) != 1 {
						errs <- fmt.Errorf("w%d r%d parse %s: %d nodes, want 1", w, round, s.path, len(snap.Nodes))
					}
					eng.SetSnapshot(snap)
					// Concurrent-read coverage: always read back, but only
					// REQUIRE presence for paths other workers never Forget.
					// d.js is forgotten by every worker's iteration, so
					// another goroutine may legitimately remove it between
					// this worker's SetSnapshot and Snapshot.
					_, ok := eng.Snapshot(s.path)
					if !ok && i != len(srcs)-1 {
						errs <- fmt.Errorf("w%d r%d snapshot %s missing after SetSnapshot", w, round, s.path)
					}
					// Forget on a subset to interleave deletes with reads.
					if i == len(srcs)-1 {
						eng.Forget(s.path)
					}
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestClose_ConcurrentWithParse mirrors shutdown: debounce timers can still
// be in flight when main's deferred Close runs. Close racing Parse and
// SetSnapshot must be free of data races and panics; post-Close Parse errors
// and SetSnapshot degrades to a no-op.
func TestClose_ConcurrentWithParse(t *testing.T) {
	eng, err := NewEngine()
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	src := []byte("package main\n\nfunc X() int { return 1 }\n")

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				snap, perr := eng.Parse("x.go", src)
				if perr == nil && snap != nil {
					eng.SetSnapshot(snap) // must not panic post-Close
				}
			}
		}()
	}

	eng.Close() // races with the workers above
	close(stop)
	wg.Wait()

	// After Close: Parse reports an error, never nil-snapshot success.
	if snap, perr := eng.Parse("x.go", src); perr == nil {
		t.Errorf("Parse after Close: err=nil snap=%v, want error", snap)
	}
	// SetSnapshot after Close is a no-op, not a nil-map panic.
	eng.SetSnapshot(&FileSnapshot{Path: "x.go", Nodes: map[string]Node{}})
	if _, ok := eng.Snapshot("x.go"); ok {
		t.Error("SetSnapshot after Close resurrected the cache")
	}
	// Close is idempotent.
	eng.Close()
}

// TestParse_UnsupportedFile asserts Parse returns (nil, nil) for files whose
// extension maps to no grammar, so the engine can skip them cheaply.
func TestParse_UnsupportedFile(t *testing.T) {
	eng := newTestEngine(t)
	snap, err := eng.Parse("notes.md", []byte("# hello"))
	if err != nil {
		t.Fatalf("unsupported file returned error: %v", err)
	}
	if snap != nil {
		t.Errorf("unsupported file returned snapshot: %+v", snap)
	}
}

func TestMultiLineDiffCounting(t *testing.T) {
	eng := newTestEngine(t)

	v1 := "package main\n\nfunc ComputeSum(a, b int) int {\n\tres := a + b\n\treturn res\n}\n"
	v2 := "package main\n\nfunc ComputeSum(a, b int) int {\n\ttemp := a * 2\n\tintermediate := b * 3\n\tfinalRes := temp + intermediate\n\treturn finalRes\n}\n"

	prev := parseOrFatal(t, eng, "calc.go", v1)
	next := parseOrFatal(t, eng, "calc.go", v2)

	res := Diff("repo", prev, next)
	if len(res.Events) != 1 {
		t.Fatalf("expected 1 modified event, got %d", len(res.Events))
	}

	ev := res.Events[0]
	if ev.Action != ActionModified {
		t.Fatalf("expected action MODIFIED, got %s", ev.Action)
	}

	if ev.AddedLines < 3 || ev.DeletedLines < 1 {
		t.Fatalf("expected multi-line diff, got AddedLines=%d DeletedLines=%d (snippet:\n%s)", ev.AddedLines, ev.DeletedLines, ev.DiffSnippet)
	}
}
