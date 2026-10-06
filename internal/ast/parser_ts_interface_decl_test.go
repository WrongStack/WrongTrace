package ast

import (
	"strings"
	"testing"
)

// Round 23: a TypeScript `interface` declaration produced no node at all.
//
// classifyNode handled "function_declaration", "method_definition",
// "arrow_function" and "class_declaration" for TypeScript/JavaScript but had
// no "interface_declaration" case, so an interface was classified as a
// non-declaration intermediate. The node never entered the snapshot, so an
// edit inside it matched no signature and emitted NO ADDED/MODIFIED/DELETED
// event: no churn analytics, no fragility/thrashing signal, no model
// attribution for that type.
//
// `interface` is a first-class TypeScript declaration and the most-used type
// form in the language; the web frontend is written in it. This is the same
// blindness class rounds 1/6/7/11/12/15/20 fixed on the GENERIC path, but on
// the NATIVE tree-sitter path -- which no round had touched.
//
// Pre-fix probe FAIL: TypeScript interface produced no node
// "class:api.ts::Repo"; got [arrow_function:api.ts::run class:api.ts::Service
// function:api.ts::build method:api.ts::Service.run], and adding a method to
// the interface emitted no Diff event attributed to it (got []).
//
// Note the signature is keyed by the REPO-RELATIVE BASE NAME: parsing
// "src/api.ts" keys nodes "api.ts::…", not "src/api.ts::…".

const tsInterfaceFixture = `interface Repo {
  find(): void;
  save(rec: string): void;
}

class Service {
  run(): void {
  }
}

function build(): void {
}

const make = (): void => {
};
`

// TestParse_TypeScriptInterfaceIsVisible pins that a TS interface produces a
// node, alongside the node types that already worked.
//
// Only TypeScript is exercised with an interface: `interface_declaration` is a
// TS-only grammar node and does not exist in tree-sitter-javascript, so the
// .js case uses plain JS and must be unaffected.
func TestParse_TypeScriptInterfaceIsVisible(t *testing.T) {
	eng, err := NewEngine()
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer eng.Close()

	snap, err := eng.Parse("api.ts", []byte(tsInterfaceFixture))
	if err != nil || snap == nil {
		t.Fatalf("parse api.ts: snap=%v err=%v", snap != nil, err)
	}
	for _, want := range []string{
		"class:api.ts::Repo",
		"class:api.ts::Service",
		"function:api.ts::build",
		"arrow_function:api.ts::make",
		// Native signatures qualify a method with its enclosing type.
		"method:api.ts::Service.run",
	} {
		if _, ok := snap.Nodes[want]; !ok {
			t.Errorf("missing %q; got %v", want, sortedSigsOf(snap))
		}
	}

	// Plain JavaScript must parse exactly as before.
	js := eng2(t)
	jsSnap, err := js.Parse("api.js", []byte(
		"class Service {\n  run() {\n  }\n}\n\nfunction build() {\n}\n"))
	if err != nil || jsSnap == nil {
		t.Fatalf("parse api.js: snap=%v err=%v", jsSnap != nil, err)
	}
	for _, want := range []string{
		"class:api.js::Service",
		"method:api.js::Service.run",
		"function:api.js::build",
	} {
		if _, ok := jsSnap.Nodes[want]; !ok {
			t.Errorf("CONTROL: api.js missing %q; got %v", want, sortedSigsOf(jsSnap))
		}
	}
}

// eng2 returns a fresh engine for a control case, failing the test on error.
func eng2(t *testing.T) *Engine {
	t.Helper()
	e, err := NewEngine()
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	t.Cleanup(e.Close)
	return e
}

// TestParse_TypeScriptInterfaceEditsEmitDiffEvents is the end-to-end
// consequence: adding a method to an interface must be attributed to it.
func TestParse_TypeScriptInterfaceEditsEmitDiffEvents(t *testing.T) {
	eng, err := NewEngine()
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer eng.Close()

	before, err := eng.Parse("api.ts", []byte(tsInterfaceFixture))
	if err != nil || before == nil {
		t.Fatalf("parse: %v", err)
	}

	edited := strings.Replace(tsInterfaceFixture,
		"  save(rec: string): void;\n", "  save(rec: string): void;\n  delete(id: string): void;\n", 1)
	if edited == tsInterfaceFixture {
		t.Fatal("fixture edit did not apply")
	}
	after, err := eng.Parse("api.ts", []byte(edited))
	if err != nil || after == nil {
		t.Fatalf("reparse: %v", err)
	}

	found := false
	for _, ev := range Diff("p", before, after).Events {
		if ev.Signature == "class:api.ts::Repo" {
			found = true
		}
	}
	if !found {
		var got []string
		for _, ev := range Diff("p", before, after).Events {
			got = append(got, ev.Signature)
		}
		t.Errorf("no Diff event attributed to class:api.ts::Repo; got %v", got)
	}
}

// TestParse_TypeAliasIsNotInvented guards the other direction: a `type X = Y`
// alias is a different tree-sitter node type, carries no body to diff, and must
// not start producing nodes as a side effect of this fix.
func TestParse_TypeAliasIsNotInvented(t *testing.T) {
	eng, err := NewEngine()
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer eng.Close()

	snap, err := eng.Parse("types.ts", []byte("type RepoId = string;\n"))
	if err != nil || snap == nil {
		t.Fatalf("parse: %v", err)
	}
	if n, ok := snap.Nodes["class:types.ts::RepoId"]; ok {
		t.Errorf("a `type` alias minted a node: %+v", n)
	}
}

// TestParse_NativeOtherLanguagesUnaffected re-pins the languages whose native
// grammars this TS/JS-only change cannot reach, plus a slice of the generic
// path's rounds 1-21 coverage.
func TestParse_NativeOtherLanguagesUnaffected(t *testing.T) {
	eng, err := NewEngine()
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	defer eng.Close()

	for _, c := range []struct {
		path string
		src  string
		want []string
	}{
		{"g.go", "package main\n\ntype Server struct{}\n\nfunc run() {}\n", []string{"struct:g.go::Server", "function:g.go::run"}},
		{"g.py", "class Repo:\n    def find(self):\n        return 1\n", []string{"class:g.py::Repo", "function:g.py::Repo.find"}},
		{"g.ts", "function build(): void {\n}\n", []string{"function:g.ts::build"}},
		{"g.js", "class Service {\n  run() {\n  }\n}\n", []string{"class:g.js::Service", "method:g.js::Service.run"}},
		{"i.java", "interface Repo {\n    int find();\n}\n", []string{"class:i.java::Repo"}},
		{"i.kt", "enum class Color {\n    RED,\n}\n", []string{"class:i.kt::Color"}},
		{"i.rb", "def valid?; @ok; end\n", []string{"function:i.rb::valid?"}},
		{"i.php", "trait Reusable {\n    public function hello() {\n        return 1;\n    }\n}\n", []string{"class:i.php::Reusable"}},
	} {
		snap, err := eng.Parse(c.path, []byte(c.src))
		if err != nil || snap == nil {
			t.Errorf("CONTROL: parse %s failed: %v", c.path, err)
			continue
		}
		for _, w := range c.want {
			if _, ok := snap.Nodes[w]; !ok {
				t.Errorf("CONTROL: %s lost node %q; got %v", c.path, w, sortedSigsOf(snap))
			}
		}
	}
}
