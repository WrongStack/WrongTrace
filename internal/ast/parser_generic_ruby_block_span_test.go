package ast

import (
	"strings"
	"testing"
)

// Round 19 (completing round 18): Ruby blocks close with `end`, never `}`.
//
// parseGenericSource widened a node past its declaration line ONLY when that
// line contained a "{" (parser.go:472). Every Ruby declaration therefore kept
// endLine on its `def`/`class` line:
//
//	function:R.rb::instance_method   lines=2..2 loc=1
//	class:R.rb::Repo                 lines=1..1 loc=1
//	function:T.php::f                lines=2..4 loc=3   <- braced, already correct
//
// The node's BodyHash is computed from that span, and Diff decides MODIFIED by
// comparing BodyHash. A one-line span freezes the hash over the `def` line, so
// no edit inside ANY Ruby method or class body could ever emit an event:
// Ruby's churn analytics, fragility/thrashing signal and model attribution were
// structurally dark. It failed silently -- the declaration WAS in the snapshot,
// it simply never changed.
//
// Pre-fix probe FAIL: "a Ruby method spans 2..2, want 2..4" and "editing the
// body of a Ruby method emitted no Diff event ...; got []".

const rubySpanFixture = `class Repo
  def make
    1
  end

  def self.find
    2
  end
end
`

// TestParseGenericSource_RubyBlockSpanCoversBody pins that a Ruby declaration's
// span includes its body. This is the assertion that keeps the hash live.
func TestParseGenericSource_RubyBlockSpanCoversBody(t *testing.T) {
	snap := parseGenericSource("Repo.rb", []byte(rubySpanFixture), LangRuby)
	for sig, want := range map[string][2]uint32{
		"function:Repo.rb::make": {2, 4},
		"function:Repo.rb::find": {6, 8},
		"class:Repo.rb::Repo":    {1, 9},
	} {
		n, ok := snap.Nodes[sig]
		if !ok {
			t.Errorf("missing node %q; got %v", sig, sortedSigsOf(snap))
			continue
		}
		if n.StartLine != want[0] || n.EndLine != want[1] {
			t.Errorf("%s spans %d..%d, want %d..%d", sig, n.StartLine, n.EndLine, want[0], want[1])
		}
	}
}

// TestParseGenericSource_RubyBodyEditEmitsDiffEvents is the end-to-end
// consequence that round 18 could not assert: an edit inside a Ruby method body
// must be attributed to that method.
func TestParseGenericSource_RubyBodyEditEmitsDiffEvents(t *testing.T) {
	before := parseGenericSource("Repo.rb", []byte(rubySpanFixture), LangRuby)

	edited := strings.Replace(rubySpanFixture, "    1\n", "    99\n", 1)
	if edited == rubySpanFixture {
		t.Fatal("fixture edit did not apply")
	}
	after := parseGenericSource("Repo.rb", []byte(edited), LangRuby)

	found := false
	for _, ev := range Diff("p", before, after).Events {
		if ev.Signature == "function:Repo.rb::make" {
			found = true
		}
	}
	if !found {
		var got []string
		for _, ev := range Diff("p", before, after).Events {
			got = append(got, ev.Signature)
		}
		t.Errorf("no Diff event attributed to function:Repo.rb::make; got %v", got)
	}
}

// TestParseGenericSource_RubyNestedBlockClosesAtOuterEnd pins that the matching
// `end` is chosen by the DECLARATION's own indentation, so a nested `if`
// inside a `def` cannot close the outer node early.
func TestParseGenericSource_RubyNestedBlockClosesAtOuterEnd(t *testing.T) {
	snap := parseGenericSource("N.rb", []byte(
		"def outer\n  if x\n    1\n  end\n  2\nend\n"), LangRuby)
	n, ok := snap.Nodes["function:N.rb::outer"]
	if !ok {
		t.Fatalf("missing node function:N.rb::outer; got %v", sortedSigsOf(snap))
	}
	if n.StartLine != 1 || n.EndLine != 6 {
		t.Errorf("outer def spans %d..%d, want 1..6 (it must not close at the inner `end`)",
			n.StartLine, n.EndLine)
	}
}

// TestParseGenericSource_RubySpanDoesNotRunAwayAndBracesAreUnchanged guards
// both ends of the change: a def with no closing `end` must not swallow the
// file, and every braced language must keep the span the brace scan produced.
func TestParseGenericSource_RubySpanDoesNotRunAwayAndBracesAreUnchanged(t *testing.T) {
	orphan := parseGenericSource("O.rb", []byte("def orphan\n  1\n"), LangRuby)
	if n := orphan.Nodes["function:O.rb::orphan"]; n.EndLine > 2 {
		t.Errorf("a def with no closing `end` spans to line %d; it must not run to EOF",
			n.EndLine)
	}

	sibling := parseGenericSource("O2.rb", []byte("def a\n  1\nend\n\nclass After\nend\n"), LangRuby)
	if _, ok := sibling.Nodes["class:O2.rb::After"]; !ok {
		t.Errorf("a later declaration was swallowed; got %v", sortedSigsOf(sibling))
	}

	php := parseGenericSource("Svc.php", []byte(
		"class Svc {\n    public function f() {\n        return 1;\n    }\n}\n"), LangPHP)
	for sig, want := range map[string][2]uint32{
		"function:Svc.php::f": {2, 4},
		"class:Svc.php::Svc":  {1, 5},
	} {
		n, ok := php.Nodes[sig]
		if !ok {
			t.Errorf("CONTROL: PHP lost %q; got %v", sig, sortedSigsOf(php))
			continue
		}
		if n.StartLine != want[0] || n.EndLine != want[1] {
			t.Errorf("CONTROL: PHP %s spans %d..%d, want %d..%d",
				sig, n.StartLine, n.EndLine, want[0], want[1])
		}
	}
}
