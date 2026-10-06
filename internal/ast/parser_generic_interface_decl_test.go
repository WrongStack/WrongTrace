package ast

// Regression (generic-interface-decl): parseGenericSource's type-declaration arm
// matched only `class` — literally `HasPrefix(trimmed, "class ")` or
// `Contains(trimmed, " class ")`. Java, C# and Dart have no compiled Tree-sitter
// grammar (NewEngine registers only Go/TS/JS/Python), so every `interface`
// declaration in those languages reached the arm list and matched nothing.
// The declaration was therefore absent from the snapshot, so an edit anywhere
// inside it matched no signature and emitted NO ADDED/MODIFIED/DELETED Diff
// event: no churn analytics, no thrashing/fragility signal, no model
// attribution. This is the same blindness round 89 fixed for Kotlin `fun` and
// round 4 fixed for qualified Rust declarations.
//
// Pre-fix probe FAIL: `public interface Repo { … }` parsed to 0 nodes, and
// adding a method to its body produced 0 Diff events while the identical edit
// inside a `class` produced 2.

import (
	"testing"
)

// TestParseGenericSource_InterfaceDeclarationsAreVisible pins that a Java/C#/Dart
// `interface` produces a node and that an edit inside it emits a Diff event.
func TestParseGenericSource_InterfaceDeclarationsAreVisible(t *testing.T) {
	eng, err := NewEngine()
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	defer eng.Close()

	cases := []struct {
		path string
		src  string
		want string
	}{
		{
			path: "src/main/java/Repo.java",
			src:  "public interface Repo {\n    String find(String id);\n}\n",
			want: "class:Repo.java::Repo",
		},
		{
			path: "Repo.cs",
			src:  "public interface IRepo {\n    string Find(string id);\n}\n",
			want: "class:Repo.cs::IRepo",
		},
		{
			path: "lib/repo.dart",
			src:  "abstract interface Repo {\n  String find(String id);\n}\n",
			want: "class:repo.dart::Repo",
		},
	}
	for _, c := range cases {
		snap, err := eng.Parse(c.path, []byte(c.src))
		if err != nil || snap == nil {
			t.Fatalf("parse %s: snap=%v err=%v", c.path, snap != nil, err)
		}
		if _, ok := snap.Nodes[c.want]; !ok {
			t.Errorf("%s missing node %q; got %v", c.path, c.want, sortedNodeSigs(snap))
		}
	}

	// The end-to-end contract: an edit inside an interface must emit an event.
	before, err := eng.Parse("src/main/java/Repo.java",
		[]byte("public interface Repo {\n    String find(String id);\n}\n"))
	if err != nil {
		t.Fatalf("parse before: %v", err)
	}
	after, err := eng.Parse("src/main/java/Repo.java",
		[]byte("public interface Repo {\n    String find(String id);\n    String findAll();\n}\n"))
	if err != nil {
		t.Fatalf("parse after: %v", err)
	}
	if events := Diff("p", before, after).Events; len(events) == 0 {
		t.Error("editing an interface body emitted 0 Diff events, want > 0")
	}
}

// TestParseGenericSource_InterfaceIsNotStolenFromNonDeclarations is the
// neighbouring control: ordinary Java statements that merely mention an
// interface-shaped type must not become declarations.
func TestParseGenericSource_InterfaceIsNotStolenFromNonDeclarations(t *testing.T) {
	src := "class Impl implements Repo {\n    Repo lookup(String id) {\n        return null;\n    }\n\n    void use() {\n        Repo r = new Repo() { };\n    }\n}\n"
	snap := parseGenericSource("src/main/java/Impl.java", []byte(src), LangJava)
	// `use` is a real method the typed arm already resolves; the anonymous
	// `new Repo() { }` inside it is not a declaration. What must never appear is
	// a node invented from a line that merely mentions an interface-shaped type.
	want := map[string]bool{
		"class:Impl.java::Impl":      true,
		"function:Impl.java::lookup": true,
		"function:Impl.java::use":    true,
	}
	for sig := range snap.Nodes {
		if !want[sig] {
			t.Errorf("unexpected node %q; interface arm invented a declaration", sig)
		}
	}
	for sig := range want {
		if _, ok := snap.Nodes[sig]; !ok {
			t.Errorf("real declaration %q missing; got %v", sig, sortedNodeSigs(snap))
		}
	}
}

func sortedNodeSigs(s *FileSnapshot) []string {
	out := make([]string, 0, len(s.Nodes))
	for sig := range s.Nodes {
		out = append(out, sig)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
