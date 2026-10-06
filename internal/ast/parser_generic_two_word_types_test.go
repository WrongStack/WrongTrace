package ast

// Regression (round 10): typeDeclName scans for the FIRST type keyword and reads
// the NEXT field as the declared name. Kotlin and Java both spell some types
// with TWO keywords — `enum class Color`, `enum interface Op` — so with `enum`
// added to the keyword set in round 6, parts = ["enum","class","Color","{"]
// matched at j=0 and read parts[1] == "class" as the NAME. The snapshot gained a
// node called `class` and lost the real `Color`, which then produced no
// ADDED/MODIFIED/DELETED Diff event on edit and carried no churn, thrashing or
// attribution signal.
//
// This is a REGRESSION introduced by round 6, not a pre-existing gap: before
// that round the arm matched only `class`, scanned every field, found "class" at
// j=1 and named the type correctly. Round 6 added the keyword but no test
// covered the two-word spelling, so nothing caught it. Both the two-word forms
// and the single-word forms are pinned below so this cannot recur silently.

import (
	"testing"
)

// TestParseGenericSource_TwoWordTypeDeclarationsAreNamedCorrectly pins that
// `enum class` / `enum interface` name the real type and mint no keyword-named node.
func TestParseGenericSource_TwoWordTypeDeclarationsAreNamedCorrectly(t *testing.T) {
	for _, c := range []struct{ path, src, want, wrong string }{
		{"Color.kt", "enum class Color {\n    RED,\n    GREEN\n}\n",
			"class:Color.kt::Color", "class:Color.kt::class"},
		{"Nested.java", "public enum class Nested {\n    A\n}\n",
			"class:Nested.java::Nested", "class:Nested.java::class"},
		{"Op.java", "enum interface Op {\n    void run();\n}\n",
			"class:Op.java::Op", "class:Op.java::interface"},
	} {
		snap := parseGenericSource(c.path, []byte(c.src), langOfTest(c.path))
		if _, ok := snap.Nodes[c.want]; !ok {
			t.Errorf("%s: two-word type declaration missing node %q; got %v",
				c.path, c.want, sortedSigsOf(snap))
		}
		if _, bad := snap.Nodes[c.wrong]; bad {
			t.Errorf("%s: phantom node %q — the scan read the second keyword as the type's name; got %v",
				c.path, c.wrong, sortedSigsOf(snap))
		}
	}
}

// TestParseGenericSource_SingleWordTypeDeclarationsUnaffected is the control:
// every form that worked before this fix must keep working.
func TestParseGenericSource_SingleWordTypeDeclarationsUnaffected(t *testing.T) {
	for _, c := range []struct{ path, src, want string }{
		{"S.java", "class S {\n}\n", "class:S.java::S"},
		{"E2.java", "enum Plain {\n    X\n}\n", "class:E2.java::Plain"},
		{"I.java", "interface I {\n    void f();\n}\n", "class:I.java::I"},
		{"D.kt", "data class User(val id: Int) {\n}\n", "class:D.kt::User"},
		{"F.kt", "class Repo {\n    fun find(id: String): String {\n        return id\n    }\n}\n",
			"class:F.kt::Repo"},
	} {
		snap := parseGenericSource(c.path, []byte(c.src), langOfTest(c.path))
		if _, ok := snap.Nodes[c.want]; !ok {
			t.Errorf("%s: missing %q; got %v", c.path, c.want, sortedSigsOf(snap))
		}
	}
}

// TestParseGenericSource_TwoWordTypesEmitDiffEvents pins the end-to-end contract
// for the form this regression silently broke.
func TestParseGenericSource_TwoWordTypesEmitDiffEvents(t *testing.T) {
	eng, err := NewEngine()
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	defer eng.Close()

	before, err := eng.Parse("E.kt", []byte("enum class Color {\n    RED\n}\n"))
	if err != nil || before == nil {
		t.Fatalf("parse before: %v", err)
	}
	after, err := eng.Parse("E.kt", []byte("enum class Color {\n    RED,\n    BLUE\n}\n"))
	if err != nil || after == nil {
		t.Fatalf("parse after: %v", err)
	}
	if events := Diff("p", before, after).Events; len(events) == 0 {
		t.Errorf("adding a variant to `enum class Color` emitted 0 Diff events; got nodes %v",
			sortedSigsOf(after))
	}
}

func langOfTest(path string) Language {
	switch {
	case hasSuffixTest(path, ".kt"):
		return LangKotlin
	case hasSuffixTest(path, ".java"):
		return LangJava
	case hasSuffixTest(path, ".rs"):
		return LangRust
	}
	return LangJava
}

func hasSuffixTest(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
