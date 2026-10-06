package ast

import (
	"strings"
	"testing"
)

// Round 17 (completing round 16): PHP 8 attributes open with `#[`, and the
// generic parser treated `#` as a line-comment marker in TWO places.
//
//  1. parseGenericSource's line filter skipped every line beginning with `#`, so
//     `#[Deprecated] public function old2() {` was discarded as a comment and
//     the declaration never entered the snapshot (fixed in round 16).
//
//  2. braceScanner.lineDelta ALSO opened a comment at `#`, so the declaration's
//     trailing `{` was skipped as comment text. braceCount stayed 0, endLine
//     never advanced past the declaration line, and the node's body hash was
//     frozen over that single line. Diff compares BodyHash, so it saw an
//     unchanged body and emitted NO event for any edit inside the method --
//     the declaration was visible but permanently mute.
//
// Diagnostic evidence (round 17):
//
//	attributed  function:Svc.php::inline    lines=2..2 loc=1  hash 252f45… (identical before/after)
//	control     function:Plain.php::plain   lines=2..4 loc=3  -> MODIFIED
//
// Both faults share one root cause, and both sites now exempt `#[`.

const phpAttrFixture = "class Svc {\n" +
	"    #[Deprecated] public function inline() {\n" +
	"        return 2;\n" +
	"    }\n" +
	"    #[A] #[B] final public function two() {\n" +
	"        return 3;\n" +
	"    }\n" +
	"    public function plain() {\n" +
	"        return 4;\n" +
	"    }\n" +
	"}\n"

// TestParseGenericSource_PhpInlineAttributeIsVisible pins fault 1: an inline
// attribute must not hide the declaration it precedes.
func TestParseGenericSource_PhpInlineAttributeIsVisible(t *testing.T) {
	snap := parseGenericSource("Svc.php", []byte(phpAttrFixture), LangPHP)
	for _, want := range []string{
		"function:Svc.php::inline",
		"function:Svc.php::two",
		"function:Svc.php::plain",
		"class:Svc.php::Svc",
	} {
		if _, ok := snap.Nodes[want]; !ok {
			t.Errorf("missing node %q; got %v", want, sortedSigsOf(snap))
		}
	}
}

// TestParseGenericSource_PhpInlineAttributeBodySpanIsTracked pins fault 2: the
// attributed node's line range must cover its body, not just its declaration
// line. A one-line span freezes the hash and silences Diff forever.
func TestParseGenericSource_PhpInlineAttributeBodySpanIsTracked(t *testing.T) {
	snap := parseGenericSource("Svc.php", []byte(phpAttrFixture), LangPHP)
	// inline is declared on line 2 and two on line 5 of phpAttrFixture.
	for sig, wantStart := range map[string]uint32{
		"function:Svc.php::inline": 2,
		"function:Svc.php::two":    5,
	} {
		n, ok := snap.Nodes[sig]
		if !ok {
			t.Errorf("missing node %q; got %v", sig, sortedSigsOf(snap))
			continue
		}
		if n.StartLine != wantStart {
			t.Errorf("%s StartLine = %d, want %d", sig, n.StartLine, wantStart)
		}
		if n.EndLine <= n.StartLine {
			t.Errorf("%s spans lines %d..%d: the body is not covered, so its hash "+
				"never changes and Diff can never report an edit inside it",
				sig, n.StartLine, n.EndLine)
		}
	}
}

// TestParseGenericSource_PhpInlineAttributeEditsEmitDiffEvents is the
// end-to-end consequence that was still failing when round 16 ended.
func TestParseGenericSource_PhpInlineAttributeEditsEmitDiffEvents(t *testing.T) {
	before := parseGenericSource("Svc.php", []byte(phpAttrFixture), LangPHP)

	edited := strings.Replace(phpAttrFixture, "        return 2;", "        return 22;", 1)
	if edited == phpAttrFixture {
		t.Fatal("fixture edit did not apply")
	}
	after := parseGenericSource("Svc.php", []byte(edited), LangPHP)

	found := false
	for _, ev := range Diff("p", before, after).Events {
		if ev.Signature == "function:Svc.php::inline" {
			found = true
		}
	}
	if !found {
		var got []string
		for _, ev := range Diff("p", before, after).Events {
			got = append(got, ev.Signature)
		}
		t.Errorf("no Diff event attributed to function:Svc.php::inline; got %v", got)
	}
}

// TestParseGenericSource_PhpHashCommentIsStillAComment guards the two-site fix
// in the other direction: a real `#` comment must still be skipped, and must
// not mint a node or swallow the declaration on the following line.
func TestParseGenericSource_PhpHashCommentIsStillAComment(t *testing.T) {
	src := "class Svc {\n" +
		"    # just a comment\n" +
		"    public function real() {\n" +
		"        return 1;\n" +
		"    }\n" +
		"}\n"
	snap := parseGenericSource("Svc.php", []byte(src), LangPHP)
	if _, ok := snap.Nodes["function:Svc.php::real"]; !ok {
		t.Errorf("a `#` comment line broke the declaration after it; got %v", sortedSigsOf(snap))
	}
	if n := len(snap.Nodes); n != 2 { // the class and the method
		t.Errorf("expected exactly 2 nodes (class + method), got %d: %v", n, sortedSigsOf(snap))
	}
}
