package ast

// Regression (round 8): parseGenericSource's type-declaration arm was selected
// by a bare SUBSTRING test — `strings.Contains(trimmed, " class ")` and its
// `interface`/`enum` siblings. That fires on any line that merely MENTIONS the
// word, including inside a quoted string literal:
//
//	System.out.println("the class is not found");  ->  class:Repo.java::is
//	warn("unknown enum value");                   ->  class:Repo.java::value");
//
// Each phantom entered the snapshot and emitted ADDED/DELETED Diff events for
// symbols that do not exist, corrupting churn analytics, fragility scoring and
// model attribution. Error messages naming "class"/"enum"/"interface" are
// ordinary in real code.
//
// Both tokens in those examples are perfectly well-formed identifiers, so a
// name-plausibility check alone CANNOT reject them — the keyword also has to sit
// in DECLARATION position, i.e. every whitespace field before it must be a
// modifier. `typeDeclModifiersOnly` enforces that; `typeDeclPlausibleName` then
// rejects whatever survives as an impossible identifier.
//
// The repo had already fixed this same failure mode for statement heads
// (parser_generic_regression_test.go), never for string literals — and rounds 1
// and 6 widened this very arm without adding the guard.

import (
	"testing"
)

// TestParseGenericSource_NoPhantomDeclFromStringLiterals is the bug: a keyword
// quoted inside a message must not mint a declaration.
func TestParseGenericSource_NoPhantomDeclFromStringLiterals(t *testing.T) {
	src := "class Repo {\n" +
		"    boolean exists(String s) {\n" +
		"        return s != null;\n" +
		"    }\n" +
		"\n" +
		"    void log() {\n" +
		"        System.out.println(\"the class is not found\");\n" +
		"        warn(\"unknown enum value\");\n" +
		"        emit(\"no interface here\");\n" +
		"    }\n" +
		"}\n"
	snap := parseGenericSource("src/main/java/Repo.java", []byte(src), LangJava)

	want := map[string]bool{
		"class:Repo.java::Repo":      true,
		"function:Repo.java::exists": true,
		"function:Repo.java::log":    true,
	}
	for sig := range snap.Nodes {
		if !want[sig] {
			t.Errorf("phantom node %q minted from a word inside a string literal; got %v",
				sig, sortedSigsOf(snap))
		}
	}
	for sig := range want {
		if _, ok := snap.Nodes[sig]; !ok {
			t.Errorf("real declaration %q missing; got %v", sig, sortedSigsOf(snap))
		}
	}
}

// TestParseGenericSource_NoPhantomDeclFromComments pins the same rule for a
// keyword appearing in a comment.
func TestParseGenericSource_NoPhantomDeclFromComments(t *testing.T) {
	src := "class C {\n" +
		"    // the enum here is prose, not a declaration\n" +
		"    void go() {\n" +
		"    }\n" +
		"}\n"
	snap := parseGenericSource("src/main/java/C.java", []byte(src), LangJava)
	got := sortedSigsOf(snap)
	if len(got) != 2 || got[0] != "class:C.java::C" || got[1] != "function:C.java::go" {
		t.Errorf("nodes = %v, want exactly [class:C.java::C function:C.java::go]", got)
	}
}

// TestTypeDeclModifiersOnlyAndPlausibleName unit-tests the two guards directly,
// including the cases that must keep working.
func TestTypeDeclModifiersOnlyAndPlausibleName(t *testing.T) {
	for _, prefix := range [][]string{
		{},                     // `class Foo` — keyword first
		{"public"},             // `public class Foo`
		{"public", "final"},    // `public final class Foo`
		{"abstract"},           // Dart `abstract class Foo`
		{"internal", "sealed"}, // `internal sealed interface I`
		{"pub"},                // Rust `pub enum E`
		{"pub(crate)"},         // Rust parenthesised visibility
	} {
		if !typeDeclModifiersOnly(prefix) {
			t.Errorf("typeDeclModifiersOnly(%v) = false, want true", prefix)
		}
	}
	for _, prefix := range [][]string{
		{`System.out.println("the`}, // the string-literal case
		{"="},                       // `x = class Foo`
		{"Runnable"},                // `Runnable r = new class`
		{"var"},                     // Swift `var class` is not a type head here
	} {
		if typeDeclModifiersOnly(prefix) {
			t.Errorf("typeDeclModifiersOnly(%v) = true, want false", prefix)
		}
	}

	for _, n := range []string{"Repo", "Color", "Foo::bar", "Foo::~Foo"} {
		if !typeDeclPlausibleName(n) {
			t.Errorf("typeDeclPlausibleName(%q) = false, want true", n)
		}
	}
	for _, n := range []string{"", `value");`, "=", `"`, "1x"} {
		if typeDeclPlausibleName(n) {
			t.Errorf("typeDeclPlausibleName(%q) = true, want false", n)
		}
	}
}

// TestParseGenericSource_ModifierAndPriorRoundsCoverageSurvives is the control:
// every declaration shape earlier rounds fixed must still resolve through the
// now-stricter arm, including qualified C++ names and Rust's pub(crate).
func TestParseGenericSource_ModifierAndPriorRoundsCoverageSurvives(t *testing.T) {
	for _, c := range []struct{ path, src, want string }{
		{"A.java", "public final class A {\n}\n", "class:A.java::A"},
		{"B.java", "public interface B {\n    String f(String s);\n}\n", "class:B.java::B"},
		{"C.java", "public enum C {\n    X,\n}\n", "class:C.java::C"},
		{"D.cs", "internal sealed interface D\n{\n    string F(string s);\n}\n", "class:D.cs::D"},
		{"E.dart", "abstract class E {\n  int g() => 1;\n}\n", "class:E.dart::E"},
		{"F.cpp", "class F {\npublic:\n    int h() {\n        return 0;\n    }\n};\n", "class:F.cpp::F"},
		{"G.rs", "pub(crate) enum G {\n    A,\n}\n", "class:G.rs::G"},
	} {
		snap := parseGenericSource(c.path, []byte(c.src), langOf(c.path))
		if _, ok := snap.Nodes[c.want]; !ok {
			t.Errorf("%s: missing %q; got %v", c.path, c.want, sortedSigsOf(snap))
		}
	}
}

func langOf(path string) Language {
	switch {
	case len(path) > 5 && path[len(path)-5:] == ".java":
		return LangJava
	case len(path) > 3 && path[len(path)-3:] == ".cs":
		return LangCSharp
	case len(path) > 5 && path[len(path)-5:] == ".dart":
		return LangDart
	case len(path) > 4 && path[len(path)-4:] == ".cpp":
		return LangCpp
	case len(path) > 3 && path[len(path)-3:] == ".rs":
		return LangRust
	}
	return LangUnknown
}
