package ast

import (
	"testing"
)

// Round 21: a one-line Ruby method had its BODY glued to the declared name.
//
// The Ruby arm takes parts[1] and splits on "(", which works while the
// definition continues onto later lines:
//
//	def find_by_name(name)  -> parts[1] = "find_by_name(name)" -> "find_by_name"
//
// But a ONE-LINE definition leaves the body on the name token:
//
//	def valid?; @ok; end    -> parts[1] = "valid?";  -> name "valid?;"
//	def tiny; 1; end        -> parts[1] = "tiny;"    -> name "tiny;"
//
// So the node was keyed by something that is not an identifier. Note the
// asymmetry that hid this for twenty rounds: `def save!(rec); rec; end` has a
// parameter list, so the existing "(" split already produced the clean "save!"
// — only parenless one-liners broke.
//
// Generic-path signatures are keyed by name, so the real identifier never
// entered the snapshot: one method's identity split in two, edits attributed
// to a phantom, and churn/thrashing/model-attribution never aggregating under
// the name an agent actually edits. Same class round 13 fixed for PHP's `&`
// and `:` sigils and round 18 for Ruby's `self.` receiver.
//
// Pre-fix probe FAIL: got [class:M.rb::M function:M.rb::make; function:M.rb::save!
// function:M.rb::tiny; function:M.rb::valid?;]

const rubyOneLinerFixture = `class M
  def valid?; @ok; end
  def save!(rec); rec; end
  def tiny; 1; end
  def self.make; 2; end
end
`

// TestParseGenericSource_RubyOneLinerNameIsIdentifier pins that a parenless
// one-line method is keyed by its bare identifier.
func TestParseGenericSource_RubyOneLinerNameIsIdentifier(t *testing.T) {
	snap := parseGenericSource("M.rb", []byte(rubyOneLinerFixture), LangRuby)
	for _, want := range []string{
		"function:M.rb::valid?",
		"function:M.rb::save!",
		"function:M.rb::tiny",
		"function:M.rb::make", // `def self.make; 2; end` also lost its receiver
	} {
		if _, ok := snap.Nodes[want]; !ok {
			t.Errorf("missing node %q; got %v", want, sortedSigsOf(snap))
		}
	}
	for _, bad := range []string{
		"function:M.rb::valid?;",
		"function:M.rb::save!;",
		"function:M.rb::tiny;",
		"function:M.rb::self.make;",
	} {
		if _, ok := snap.Nodes[bad]; ok {
			t.Errorf("the body leaked into the declared name %q; got %v", bad, sortedSigsOf(snap))
		}
	}
}

// TestRubyMethodName_KeepsRubyIdentifierForms unit-pins the trimmer itself.
// Ruby method names are letters/digits/'_' and may end with '?' or '!'
// (predicate, bang) or '=' (setter, `def name=(v)`), so those must survive.
func TestRubyMethodName_KeepsRubyIdentifierForms(t *testing.T) {
	for tok, want := range map[string]string{
		"valid?":     "valid?",
		"save!":      "save!",
		"tiny;":      "tiny",
		"name=":      "name=",
		"find":       "find",
		"a1_b":       "a1_b",
		"valid?;":    "valid?",
		"save!(rec)": "save!", // '!' is a valid name char; '(' is the first invalid one
		"":           "",
		";":          "",
	} {
		if got := rubyMethodName(tok); got != want {
			t.Errorf("rubyMethodName(%q) = %q, want %q", tok, got, want)
		}
	}
}

// TestParseGenericSource_RubyMultiLineShapesUnchanged re-pins every Ruby shape
// that already worked, including the setter form this trimmer must not break.
func TestParseGenericSource_RubyMultiLineShapesUnchanged(t *testing.T) {
	for _, c := range []struct {
		path string
		src  string
		want []string
	}{
		{"a.rb", "def run\n  1\nend\n", []string{"function:a.rb::run"}},
		{"b.rb", "def find_by_name(name)\n  1\nend\n", []string{"function:b.rb::find_by_name"}},
		{"c.rb", "def save!(record)\n  1\nend\n", []string{"function:c.rb::save!"}},
		{"d.rb", "def self.find\n  1\nend\n", []string{"function:d.rb::find"}},
		{"e.rb", "class Widget\n  def render\n  end\nend\n", []string{"class:e.rb::Widget", "function:e.rb::render"}},
		{"f.rb", "def value = 42\n", []string{"function:f.rb::value"}},
		{"g.rb", "def name=(v)\n  1\nend\n", []string{"function:g.rb::name="}},
		// Round 20: module visibility.
		{"h.rb", "module Utils\n  def helper\n    1\n  end\nend\n", []string{"class:h.rb::Utils", "function:h.rb::helper"}},
		// Non-Ruby arms this change must not touch.
		{"i.php", "class Svc {\n    public function &byRef() {\n        return 1;\n    }\n}\n", []string{"function:i.php::byRef"}},
		{"j.kt", "class Greeter {\n  fun greet(): String {\n    return \"hi\"\n  }\n}\n", []string{"class:j.kt::Greeter", "function:j.kt::greet"}},
		{"k.rs", "pub trait Greeter {\n    fn hi(&self);\n}\n", []string{"class:k.rs::Greeter"}},
		{"l.java", "class Bank {\n    int balance() {\n        return 0;\n    }\n}\n", []string{"class:l.java::Bank", "function:l.java::balance"}},
	} {
		snap := parseGenericSource(c.path, []byte(c.src), DetectLanguage(c.path))
		for _, w := range c.want {
			if _, ok := snap.Nodes[w]; !ok {
				t.Errorf("%s: missing %q; got %v", c.path, w, sortedSigsOf(snap))
			}
		}
	}
}
