package ast

// Round 13: PHP declared names kept PHP signature sigils, so the node was keyed
// by something that is not the identifier and the real identifier never entered
// the snapshot.
//
//   public function &byRef()   -> keyed "&byRef"   (reference-return sigil)
//   enum Suit: string           -> keyed "Suit:"    (PHP 8.1 backed-enum type)
//
// Generic-path signatures are keyed by name, so in both cases the true
// identifier was absent: no ADDED/MODIFIED/DELETED Diff event was ever
// attributed to that method or type, and it carried no churn, thrashing or
// model-attribution signal.
//
// Pre-fix probe FAIL: no node "function:Repo.php::byRef" (got "&byRef") and no
// node "class:Suit.php::Suit" (got "Suit:"). Post-fix: PASS.

import (
	"strings"
	"testing"
)

// TestParseGenericSource_PhpByReferenceNameIsBareIdentifier pins the `&`
// reference-return case end to end, including Diff attribution.
func TestParseGenericSource_PhpByReferenceNameIsBareIdentifier(t *testing.T) {
	src := "class Repo {\n" +
		"    public function &byRef() {\n" +
		"        return $this;\n" +
		"    }\n" +
		"    public function plain() {\n" +
		"        return 1;\n" +
		"    }\n" +
		"}\n"

	snap := parseGenericSource("Repo.php", []byte(src), LangPHP)
	if _, ok := snap.Nodes["function:Repo.php::byRef"]; !ok {
		t.Errorf("by-reference method not keyed by its identifier; got %v", sortedSigsOf(snap))
	}
	if _, ok := snap.Nodes["function:Repo.php::&byRef"]; ok {
		t.Error("the PHP reference sigil leaked into the declared name")
	}
	if _, ok := snap.Nodes["function:Repo.php::plain"]; !ok {
		t.Errorf("sibling method lost its node; got %v", sortedSigsOf(snap))
	}

	// Editing the by-reference method must be attributed to the clean name.
	edited := strings.Replace(src, "return $this;", "return $this->next;", 1)
	if edited == src {
		t.Fatal("fixture edit did not apply")
	}
	after := parseGenericSource("Repo.php", []byte(edited), LangPHP)

	found := false
	for _, ev := range Diff("p", snap, after).Events {
		if ev.Signature == "function:Repo.php::byRef" {
			found = true
		}
	}
	if !found {
		var got []string
		for _, ev := range Diff("p", snap, after).Events {
			got = append(got, ev.Signature)
		}
		t.Errorf("no Diff event attributed to function:Repo.php::byRef; got %v", got)
	}
}

// TestParseGenericSource_PhpBackedEnumNameIsBareIdentifier pins the PHP 8.1
// `enum Suit: string` spelling, whose name field is literally "Suit:".
func TestParseGenericSource_PhpBackedEnumNameIsBareIdentifier(t *testing.T) {
	snap := parseGenericSource("Suit.php", []byte(
		"enum Suit: string {\n    case Hearts;\n}\nenum Plain {\n    case Spades;\n}\n"), LangPHP)

	if _, ok := snap.Nodes["class:Suit.php::Suit"]; !ok {
		t.Errorf("backed enum not keyed by its identifier; got %v", sortedSigsOf(snap))
	}
	if _, ok := snap.Nodes["class:Suit.php::Suit:"]; ok {
		t.Error("the backed-enum type colon leaked into the declared name")
	}
	if _, ok := snap.Nodes["class:Suit.php::Plain"]; !ok {
		t.Errorf("unbacked enum lost its node; got %v", sortedSigsOf(snap))
	}
}

// TestParseGenericSource_PhpArmStillTracksOrdinaryDeclarations re-pins the PHP
// shapes that already worked, so the two trims cannot quietly undo them.
func TestParseGenericSource_PhpArmStillTracksOrdinaryDeclarations(t *testing.T) {
	for _, c := range []struct {
		path string
		src  string
		want []string
	}{
		{"a.php", "function plain() {\n  return 1;\n}\n", []string{"function:a.php::plain"}},
		{"b.php", "class Svc {\n  public function pub() {\n  }\n}\n", []string{"function:b.php::pub", "class:b.php::Svc"}},
		{"c.php", "class Svc {\n  private static function privStat() {\n  }\n}\n", []string{"function:c.php::privStat"}},
		{"d.php", "class Svc {\n  final protected function finProt() {\n  }\n}\n", []string{"function:d.php::finProt"}},
		{"e.php", "interface Contract {\n}\n", []string{"class:e.php::Contract"}},
		// Ruby is the other half of this arm and must be untouched.
		{"f.rb", "def run\n  1\nend\n", []string{"function:f.rb::run"}},
		{"g.rb", "class Widget\n  def render\n  end\nend\n", []string{"class:g.rb::Widget", "function:g.rb::render"}},
	} {
		snap := parseGenericSource(c.path, []byte(c.src), DetectLanguage(c.path))
		for _, w := range c.want {
			if _, ok := snap.Nodes[w]; !ok {
				t.Errorf("%s: missing %q; got %v", c.path, w, sortedSigsOf(snap))
			}
		}
	}
}

// TestParseGenericSource_TrimmedNamesDoNotBreakOtherLanguages guards the two
// trims against collateral damage on languages that share the same helpers.
func TestParseGenericSource_TrimmedNamesDoNotBreakOtherLanguages(t *testing.T) {
	for _, c := range []struct {
		path string
		src  string
		want []string
	}{
		{"g.java", "class Bank {\n  int balance() {\n    return 0;\n  }\n}\n", []string{"class:g.java::Bank", "function:g.java::balance"}},
		{"h.kt", "class Greeter {\n  fun greet(): String {\n    return \"hi\"\n  }\n}\n", []string{"class:h.kt::Greeter", "function:h.kt::greet"}},
		{"i.rs", "pub(crate) async fn worker() {\n    let y = 2;\n}\n", []string{"function:i.rs::worker"}},
		{"j.cpp", "class Buffer {\n  char *strdup(const char *s) {\n    return s;\n  }\n}\n", []string{"function:j.cpp::strdup"}},
	} {
		snap := parseGenericSource(c.path, []byte(c.src), DetectLanguage(c.path))
		for _, w := range c.want {
			if _, ok := snap.Nodes[w]; !ok {
				t.Errorf("%s: missing %q; got %v", c.path, w, sortedSigsOf(snap))
			}
		}
	}
}
