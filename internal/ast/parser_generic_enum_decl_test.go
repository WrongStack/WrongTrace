package ast

// Regression (round 6): parseGenericSource's type-declaration arm matched only
// `class` and `interface` (the latter added in round 1). `enum` — a RESERVED
// keyword in Java, C#, C and C++, and one of the most common type declarations
// in real Java — matched no arm at all, so `public enum Color {` produced NO
// node. The declaration was absent from the snapshot, so an edit anywhere inside
// it matched no signature and emitted no ADDED/MODIFIED/DELETED Diff event: no
// churn analytics, no thrashing/fragility signal, no model attribution for any
// symbol defined in an enum.
//
// This completes round 1's keyword coverage for the brace-delimited type forms.
// It is a declaration form that round 1 never covered, not a re-report of it.
//
// `object` is deliberately NOT in the arm: it is not reserved in Java, so
// `Object object = get();` would enter the arm and take the following token
// ("=") as a declaration name. The control below pins that nothing is invented
// from a line that merely contains an enum-shaped word.

import (
	"testing"
)

// TestParseGenericSource_EnumDeclarationsAreVisible pins that a Java and C# enum
// produces a node, and that an edit inside it emits a Diff event.
func TestParseGenericSource_EnumDeclarationsAreVisible(t *testing.T) {
	eng, err := NewEngine()
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	defer eng.Close()

	java := "package p;\n\npublic enum Color {\n    RED,\n    GREEN;\n\n    public boolean isWarm() {\n        return true;\n    }\n}\n"
	cs := "public enum Level {\n    Low,\n    High\n}\n"

	for _, c := range []struct{ path, src, want string }{
		{"src/main/java/Color.java", java, "class:Color.java::Color"},
		{"Color.cs", cs, "class:Color.cs::Level"},
	} {
		snap, err := eng.Parse(c.path, []byte(c.src))
		if err != nil || snap == nil {
			t.Fatalf("parse %s: snap=%v err=%v", c.path, snap != nil, err)
		}
		if _, ok := snap.Nodes[c.want]; !ok {
			t.Errorf("%s missing node %q; got %v", c.path, c.want, sortedSigsOf(snap))
		}
	}

	// The end-to-end contract: adding a constant to an enum must be visible.
	before, err := eng.Parse("src/main/java/Color.java",
		[]byte("package p;\n\npublic enum Color {\n    RED,\n    GREEN;\n}\n"))
	if err != nil {
		t.Fatalf("parse before: %v", err)
	}
	after, err := eng.Parse("src/main/java/Color.java",
		[]byte("package p;\n\npublic enum Color {\n    RED,\n    GREEN,\n    BLUE;\n}\n"))
	if err != nil {
		t.Fatalf("parse after: %v", err)
	}
	if events := Diff("p", before, after).Events; len(events) == 0 {
		t.Error("adding a constant to an enum emitted 0 Diff events, want > 0")
	}
}

// TestParseGenericSource_EnumIsNotStolenFromNonDeclarations is the control: a
// line that merely contains an enum-shaped word must not become a declaration,
// and the sibling `class`/`interface` forms must keep resolving.
func TestParseGenericSource_EnumIsNotStolenFromNonDeclarations(t *testing.T) {
	src := "class Holder {\n    int enumField = compute();\n    int enumIndex = 0;\n}\n"
	snap := parseGenericSource("src/main/java/Holder.java", []byte(src), LangJava)
	if got := sortedSigsOf(snap); len(got) != 1 || got[0] != "class:Holder.java::Holder" {
		t.Errorf("nodes = %v, want exactly [class:Holder.java::Holder]; the enum arm invented a declaration", got)
	}

	// Round 1's coverage must be untouched by this one.
	ctl := parseGenericSource("src/main/java/Repo.java",
		[]byte("public interface Repo {\n    String find(String id);\n}\n"), LangJava)
	if _, ok := ctl.Nodes["class:Repo.java::Repo"]; !ok {
		t.Errorf("interface coverage regressed; got %v", sortedSigsOf(ctl))
	}
}

func sortedSigsOf(s *FileSnapshot) []string {
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
