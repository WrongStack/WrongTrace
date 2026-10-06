package ast

import (
	"testing"
)

// Round 20: `module` was missing from typeDeclName's keyword set, so a Ruby
// module declaration produced NO node.
//
// `module Utils` matched no arm at all -- the Ruby arm handles only `def` /
// `def self.`, and typedDeclLanguage deliberately excludes Ruby -- so the module
// never entered the snapshot: no ADDED/MODIFIED/DELETED Diff event, and no
// churn, fragility or model-attribution signal for the module itself.
//
// It failed SILENTLY, which is what made it survive sixteen rounds. The methods
// inside the module were tracked, so the snapshot looked partially populated:
//
//	pre-fix: got [class:app.rb::Repo function:app.rb::helper function:app.rb::make]
//	         -- helper and Repo present, `Utils` missing entirely
//
// Adding the keyword is safe because the arm is gated by
// typeDeclModifiersOnly, which rejects the line unless every field before the
// keyword is a modifier. `module` is NOT reserved in Java/C#/C++/Dart, so
// `int module = 5;` there must not mint a node -- pinned below across all four.

const rubyModuleFixture = `module Utils
  def helper
    1
  end
end

class Repo
  def make
    2
  end
end
`

// TestParseGenericSource_RubyModuleIsVisible pins that a Ruby module produces a
// node, alongside the members that already worked.
func TestParseGenericSource_RubyModuleIsVisible(t *testing.T) {
	snap := parseGenericSource("app.rb", []byte(rubyModuleFixture), LangRuby)
	for _, want := range []string{
		"class:app.rb::Utils",
		"function:app.rb::helper",
		"class:app.rb::Repo",
		"function:app.rb::make",
	} {
		if _, ok := snap.Nodes[want]; !ok {
			t.Errorf("missing node %q; got %v", want, sortedSigsOf(snap))
		}
	}
}

// TestParseGenericSource_ModuleKeywordIsNotStolenFromVariables pins the misfire
// risk of adding `module` to a keyword set shared by every generic language:
// `int module = 5;` is a legal variable declaration in Java, C#, C++ and Dart.
func TestParseGenericSource_ModuleKeywordIsNotStolenFromVariables(t *testing.T) {
	for _, c := range []struct{ path, src string }{
		{"M1.java", "class C {\n    int module = 5;\n}\n"},
		{"M2.cs", "class C {\n    int module = 5;\n}\n"},
		{"M3.cpp", "class C {\n    int module = 5;\n}\n"},
		{"M4.dart", "class C {\n  int module = 5;\n}\n"},
	} {
		snap := parseGenericSource(c.path, []byte(c.src), DetectLanguage(c.path))
		for _, bad := range []string{
			"function:" + c.path + "::module",
			"class:" + c.path + "::5",
			"class:" + c.path + "::=",
		} {
			if _, ok := snap.Nodes[bad]; ok {
				t.Errorf("%s: `int module = 5;` minted the phantom node %q; got %v",
					c.path, bad, sortedSigsOf(snap))
			}
		}
		if _, ok := snap.Nodes["class:"+c.path+"::C"]; !ok {
			t.Errorf("%s: lost its class node; got %v", c.path, sortedSigsOf(snap))
		}
	}
}

// TestParseGenericSource_PriorTypeKeywordsSurvive re-pins every keyword that
// already joined this set, across every language that reaches it, so this
// round's one-clause change cannot undo any of them.
func TestParseGenericSource_PriorTypeKeywordsSurvive(t *testing.T) {
	for _, c := range []struct {
		path string
		src  string
		want []string
	}{
		// Ruby, rounds 18/19.
		{"a.rb", "class W\n  def render\n  end\nend\n", []string{"class:a.rb::W", "function:a.rb::render"}},
		{"b.rb", "def self.make\n  1\nend\n", []string{"function:b.rb::make"}},
		// PHP, rounds 13/15/16/17.
		{"c.php", "trait Reusable {\n    public function hello() {\n        return 1;\n    }\n}\n", []string{"class:c.php::Reusable"}},
		{"d.php", "class Svc {\n    #[Deprecated] public function inline() {\n        return 1;\n    }\n}\n", []string{"function:d.php::inline"}},
		{"e.php", "class Svc {\n    public function &byRef() {\n        return 1;\n    }\n}\n", []string{"function:e.php::byRef"}},
		{"f.php", "enum Suit: string {\n    case Hearts;\n}\n", []string{"class:f.php::Suit"}},
		{"g.php", "class Svc {\n  public function pub() {\n  }\n}\n", []string{"class:g.php::Svc"}},
		// Kotlin, rounds 11/12.
		{"h.kt", "class Greeter {\n  fun greet(): String {\n    return \"hi\"\n  }\n}\n", []string{"class:h.kt::Greeter", "function:h.kt::greet"}},
		// Rust, round 7.
		{"i.rs", "pub trait Greeter {\n    fn hi(&self);\n}\n", []string{"class:i.rs::Greeter"}},
		// Java, round 9.
		{"j.java", "class Bank {\n    int balance() {\n        return 0;\n    }\n}\n", []string{"class:j.java::Bank", "function:j.java::balance"}},
	} {
		snap := parseGenericSource(c.path, []byte(c.src), DetectLanguage(c.path))
		for _, w := range c.want {
			if _, ok := snap.Nodes[w]; !ok {
				t.Errorf("%s: missing %q; got %v", c.path, w, sortedSigsOf(snap))
			}
		}
	}
}
