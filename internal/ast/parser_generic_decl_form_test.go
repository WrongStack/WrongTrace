package ast

import (
	"strings"
	"testing"
)

// Round 25: three declaration FORMS the generic head recogniser did not
// enumerate produced no node at all, so their bodies were invisible to churn,
// fragility and attribution. The round-24 probe disclosed them as GAPs — a
// different trigger from that round's modifier blindness: here the KEYWORD
// itself was absent from the type-declaration set, not a modifier rejected by
// the type-token guard.
//
//   - Dart `mixin M { ... }` and `extension E on String { ... }`
//   - C++ `namespace Ns { ... }`
//
// The set already carried class/interface/enum/object/trait/module. Adding the
// three must not mint a phantom for C++'s `using namespace std;`, whose prefix
// (`using`) is not a declaration modifier — that is the negative control.
func TestParseGenericSource_DeclarationFormHeadsAreRecognised(t *testing.T) {
	cases := []struct {
		name string
		file string
		lang Language
		src  string
		want string
	}{
		{"dart: mixin", "m.dart", LangDart,
			"mixin M {\n  void f() {}\n}\n", "class:m.dart::M"},
		{"dart: extension", "e.dart", LangDart,
			"extension E on String {\n  int g() => 1;\n}\n", "class:e.dart::E"},
		{"cpp: namespace", "n.cpp", LangCpp,
			"namespace Ns {\nint f() {\n  return 0;\n}\n}\n", "class:n.cpp::Ns"},
		// Controls: forms the set already carried keep working.
		{"control: dart class", "c.dart", LangDart,
			"class C {\n  int h() => 1;\n}\n", "class:c.dart::C"},
		{"control: cpp struct", "s.cpp", LangCpp,
			"struct S {\nint v;\n};\n", "struct:s.cpp::S"},
		{"control: java interface", "i.java", LangJava,
			"public interface I {\n    String f(String s);\n}\n", "class:i.java::I"},
	}
	for _, tc := range cases {
		snap := parseGenericSource(tc.file, []byte(tc.src), tc.lang)
		if _, ok := snap.Nodes[tc.want]; !ok {
			t.Errorf("%s: missing node %q; got %v", tc.name, tc.want, sortedSigsOf(snap))
		}
	}
}

// TestParseGenericSource_UsingNamespaceIsNotADeclaration is the negative
// control for adding `namespace` to the set: C++'s using-directive shares the
// keyword but is a statement, and its prefix (`using`) is not a declaration
// modifier, so the prefix guard must keep rejecting it.
func TestParseGenericSource_UsingNamespaceIsNotADeclaration(t *testing.T) {
	for _, tc := range []struct {
		file string
		lang Language
		src  string
	}{
		{"u.cpp", LangCpp, "using namespace std;\n"},
		{"u.cpp", LangCpp, "void f() {\n    using namespace std;\n}\n"},
	} {
		snap := parseGenericSource(tc.file, []byte(tc.src), tc.lang)
		for sig := range snap.Nodes {
			if strings.HasSuffix(sig, "::std") || strings.HasSuffix(sig, "::namespace") {
				t.Errorf("a using-directive became the declaration %q (source %q)", sig, tc.src)
			}
		}
	}
}
