package ast

import (
	"testing"
)

// Round 18: a Ruby singleton method was keyed by its receiver as well as its
// name.
//
// parseGenericSource's Ruby arm read parts[1] verbatim, so `def self.make`
// produced the node "function:Repo.rb::self.make". `self.` is the receiver --
// part of the signature, not part of the identifier, which is `make`.
//
// Generic-path signatures are keyed by name, so the real method name never
// entered the snapshot: one method's identity split in two, its edits
// attributed to a phantom, and its churn/thrashing/model-attribution signals
// never aggregating under the name an agent actually edits. This is the Ruby
// half of the split-identity class round 13 fixed for PHP's `&` and `:` sigils.
//
// Pre-fix probe FAIL: singleton method produced no node "function:Repo.rb::make";
// got [... function:Repo.rb::self.find_by_name function:Repo.rb::self.make].
//
// NOTE: this file pins the NAME only. Ruby `def ... end` has no brace, so
// parseGenericSource never widens a Ruby node past its declaration line -- a
// separate, pre-existing body-span defect (proved in round 18 by
// ruby-linerange: a plain `def instance_method` also gets lines=2..2 while a
// braced PHP method gets lines=2..4). Until that is fixed, no edit inside ANY
// Ruby method body produces a Diff event, so a Diff assertion here would be
// asserting a defect this round deliberately did not touch.

// TestParseGenericSource_RubySingletonIsKeyedByMethodName pins the receiver is
// stripped, with and without parameters.
func TestParseGenericSource_RubySingletonIsKeyedByMethodName(t *testing.T) {
	snap := parseGenericSource("Repo.rb", []byte(
		"class Repo\n"+
			"  def self.make\n"+
			"    1\n"+
			"  end\n"+
			"\n"+
			"  def self.find_by_name(name)\n"+
			"    2\n"+
			"  end\n"+
			"\n"+
			"  def instance_method\n"+
			"    3\n"+
			"  end\n"+
			"end\n"), LangRuby)

	for _, want := range []string{
		"function:Repo.rb::make",
		"function:Repo.rb::find_by_name",
		"function:Repo.rb::instance_method",
		"class:Repo.rb::Repo",
	} {
		if _, ok := snap.Nodes[want]; !ok {
			t.Errorf("missing node %q; got %v", want, sortedSigsOf(snap))
		}
	}
	for _, bad := range []string{
		"function:Repo.rb::self.make",
		"function:Repo.rb::self.find_by_name",
	} {
		if _, ok := snap.Nodes[bad]; ok {
			t.Errorf("the `self.` receiver leaked into the declared name %q; got %v",
				bad, sortedSigsOf(snap))
		}
	}
}

// TestParseGenericSource_RubySingletonInsideModule pins the same fix when the
// singleton is declared inside a module, where the module itself is a `module`
// keyword the generic path does not claim.
func TestParseGenericSource_RubySingletonInsideModule(t *testing.T) {
	snap := parseGenericSource("d.rb", []byte(
		"module Utils\n  def self.helper\n  end\nend\n"), LangRuby)
	if _, ok := snap.Nodes["function:d.rb::helper"]; !ok {
		t.Errorf("singleton method inside a module lost its node; got %v", sortedSigsOf(snap))
	}
	if _, ok := snap.Nodes["function:d.rb::self.helper"]; ok {
		t.Errorf("the `self.` receiver leaked into the declared name; got %v", sortedSigsOf(snap))
	}
}

// TestParseGenericSource_RubyAndPhpArmsUnaffected re-pins the shapes that
// already worked across both arms this session touched.
func TestParseGenericSource_RubyAndPhpArmsUnaffected(t *testing.T) {
	for _, c := range []struct {
		path string
		src  string
		want []string
	}{
		{"a.rb", "def run\n  1\nend\n", []string{"function:a.rb::run"}},
		{"b.rb", "class Widget\n  def render\n  end\nend\n", []string{"class:b.rb::Widget", "function:b.rb::render"}},
		{"c.rb", "def save!(record)\n  1\nend\n", []string{"function:c.rb::save!"}},
		{"d.php", "class Svc {\n    public function &byRef() {\n        return 1;\n    }\n}\n", []string{"function:d.php::byRef"}},
		{"e.php", "trait Reusable {\n    public function hello() {\n        return 1;\n    }\n}\n", []string{"class:e.php::Reusable"}},
		{"f.php", "class Svc {\n    #[Deprecated] public function inline() {\n        return 1;\n    }\n}\n", []string{"function:f.php::inline"}},
		{"g.kt", "class Greeter {\n  fun greet(): String {\n    return \"hi\"\n  }\n}\n", []string{"class:g.kt::Greeter", "function:g.kt::greet"}},
		{"h.rs", "pub(crate) async fn worker() {\n    let y = 2;\n}\n", []string{"function:h.rs::worker"}},
		{"i.java", "class Bank {\n    int balance() {\n        return 0;\n    }\n}\n", []string{"class:i.java::Bank", "function:i.java::balance"}},
	} {
		snap := parseGenericSource(c.path, []byte(c.src), DetectLanguage(c.path))
		for _, w := range c.want {
			if _, ok := snap.Nodes[w]; !ok {
				t.Errorf("%s: missing %q; got %v", c.path, w, sortedSigsOf(snap))
			}
		}
	}
}
