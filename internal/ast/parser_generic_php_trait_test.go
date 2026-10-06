package ast

import (
	"strings"
	"testing"
)

// Round 15: PHP `trait` was missing from typeDeclName's keyword set, so a
// trait declaration produced NO node at all.
//
// `trait` is a core PHP feature and a first-class type declaration alongside
// class/interface/enum. `trait Reusable { ... }` matched no arm in the generic
// path, so the declaration never entered the snapshot: no ADDED/MODIFIED/DELETED
// Diff event was emitted for any edit inside the trait, and it carried no
// churn, thrashing/fragility or model-attribution signal. The trait's METHODS
// were still tracked, so a snapshot looked partially populated rather than
// empty — the failure was silent.
//
// Pre-fix probe FAIL: PHP trait produced no node "class:Widget.php::Reusable";
// got [class:Widget.php::Button function:Widget.php::hello]. Post-fix: PASS.
//
// Adding the keyword is safe because the arm is gated by
// typeDeclModifiersOnly, which rejects the line unless every field before the
// keyword is a modifier -- pinned below by four `trait`-as-identifier misfire
// controls. (PHP itself reserves the word.)

// phpTraitFixture holds a trait, a method inside it, and a consuming class.
const phpTraitFixture = `trait Reusable {
    public function hello() {
        return "hi";
    }
}

class Button {
    use Reusable;
}
`

// TestParseGenericSource_PhpTraitIsVisible pins that a PHP trait and the
// methods inside it are all tracked.
func TestParseGenericSource_PhpTraitIsVisible(t *testing.T) {
	snap := parseGenericSource("Widget.php", []byte(phpTraitFixture), LangPHP)
	for _, want := range []string{
		"class:Widget.php::Reusable",
		"function:Widget.php::hello",
		"class:Widget.php::Button",
	} {
		if _, ok := snap.Nodes[want]; !ok {
			t.Errorf("missing node %q; got %v", want, sortedSigsOf(snap))
		}
	}
}

// TestParseGenericSource_PhpTraitEditsEmitDiffEvents is the end-to-end
// consequence: an edit inside the trait must be attributed to the trait.
func TestParseGenericSource_PhpTraitEditsEmitDiffEvents(t *testing.T) {
	before := parseGenericSource("Widget.php", []byte(phpTraitFixture), LangPHP)

	edited := strings.Replace(phpTraitFixture, `return "hi";`, `return "hello";`, 1)
	if edited == phpTraitFixture {
		t.Fatal("fixture edit did not apply")
	}
	after := parseGenericSource("Widget.php", []byte(edited), LangPHP)

	found := false
	for _, ev := range Diff("p", before, after).Events {
		if ev.Signature == "class:Widget.php::Reusable" {
			found = true
		}
	}
	if !found {
		var got []string
		for _, ev := range Diff("p", before, after).Events {
			got = append(got, ev.Signature)
		}
		t.Errorf("no Diff event attributed to class:Widget.php::Reusable; got %v", got)
	}
}

// TestParseGenericSource_TraitKeywordIsNotStolenFromVariables pins the
// misfire risk of adding `trait` to a keyword set shared by every generic
// language: `int trait = 5;` is a legal variable declaration in C++, Java,
// C# and Dart, and must not mint a node.
func TestParseGenericSource_TraitKeywordIsNotStolenFromVariables(t *testing.T) {
	cases := []struct{ path, src string }{
		{"A.cpp", "class C {\n    int trait = 5;\n}\n"},
		{"B.java", "class C {\n    int trait = 5;\n}\n"},
		{"C.cs", "class C {\n    int trait = 5;\n}\n"},
		{"D.dart", "class C {\n  int trait = 5;\n}\n"},
	}
	for _, c := range cases {
		snap := parseGenericSource(c.path, []byte(c.src), DetectLanguage(c.path))
		for _, bad := range []string{
			"function:" + c.path + "::trait",
			"class:" + c.path + "::5",
			"class:" + c.path + "::=",
		} {
			if _, ok := snap.Nodes[bad]; ok {
				t.Errorf("%s: `trait` used as a variable minted the phantom node %q; got %v",
					c.path, bad, sortedSigsOf(snap))
			}
		}
		if _, ok := snap.Nodes["class:"+c.path+"::C"]; !ok {
			t.Errorf("%s: lost its class node; got %v", c.path, sortedSigsOf(snap))
		}
	}
}

// TestParseGenericSource_PriorTypeDeclarationsSurvive re-pins every keyword
// that already joined this set, across every language that reaches it.
func TestParseGenericSource_PriorTypeDeclarationsSurvive(t *testing.T) {
	cases := []struct {
		path string
		src  string
		want []string
	}{
		// PHP type declarations, rounds 1, 6, 11 and 13.
		{"a.php", "class Thing {\n}\n", []string{"class:a.php::Thing"}},
		{"b.php", "interface Contract {\n}\n", []string{"class:b.php::Contract"}},
		{"c.php", "enum Suit: string {\n    case Hearts;\n}\n", []string{"class:c.php::Suit"}},
		{"d.php", "object Registry {\n    function register() {\n    }\n}\n", []string{"class:d.php::Registry"}},
		{"e.php", "class Svc {\n    public function &byRef() {\n        return 1;\n    }\n}\n", []string{"function:e.php::byRef"}},
		// Kotlin, round 6 and round 12.
		{"f.kt", "enum class Color {\n    RED,\n}\n", []string{"class:f.kt::Color"}},
		{"g.kt", "class Registry {\n    companion object Factory {\n        fun make(): Registry = Registry()\n    }\n}\n", []string{"class:g.kt::Factory"}},
		// Rust, round 7.
		{"h.rs", "pub trait Greeter {\n    fn hi(&self);\n}\n", []string{"class:h.rs::Greeter"}},
		// Java/Dart, round 9.
		{"i.java", "class Bank {\n    int balance() {\n        return 0;\n    }\n}\n", []string{"class:i.java::Bank", "function:i.java::balance"}},
	}
	for _, c := range cases {
		snap := parseGenericSource(c.path, []byte(c.src), DetectLanguage(c.path))
		for _, w := range c.want {
			if _, ok := snap.Nodes[w]; !ok {
				t.Errorf("%s: missing %q; got %v", c.path, w, sortedSigsOf(snap))
			}
		}
	}
}
