package db

// Regression (round 3): SymbolHistory's sigClause carried an UNANCHORED contains
// arm, `LOWER(e.node_signature) LIKE '%' || LOWER(?) || '%'`, alongside two arms
// that exist precisely to anchor a symbol name at a ':' component boundary
// (`'%.::' || ?` and `'%:' || ?`). The contains arm subsumed both, so the
// anchoring they were written to provide was defeated: any query matched every
// stored signature CONTAINING the needle.
//
// The dashboard sends the FULL stored signature verbatim (CodeAtlas.tsx:1607
// passes node_signature), so lowercased the needle is a PREFIX of any longer
// sibling. Asking for "func:DoTask" returned "func:DoTaskAsync" and
// "func:DoTaskHelper" as well, interleaving three symbols' revisions into one
// timeline — breaking the contract this function documents at queries.go:1576,
// "the revision history of a specific AST symbol / node". Same defect class the
// per-file PATH arms had ("main.go" matching "cmd/domain.go"), carried on the
// signature axis.
//
// The fix deletes the contains arm. That made the two equality arms load-bearing
// for the first time, and they had been binding escapeLike-quoted values — which
// `=` never unescapes — so every signature containing '_' or '%' stopped matching
// at all. sigArgs now binds RAW on the equality arms and the quoted value only on
// the LIKE arms, which are the only ones declaring ESCAPE (round 77's invariant).
// The metacharacter controls below are what caught that regression; without them
// the fix would have shipped a silent "symbol not found" for ordinary snake_case
// signatures, which are the overwhelming majority in this codebase.

import (
	"testing"
	"time"
)

func seedSig(t *testing.T, st *Store, id, path, sig string, at time.Time) {
	t.Helper()
	if err := st.InsertEvent(EventRecord{
		EventID: id, RepoName: "meta", FilePath: path, Signature: sig,
		NodeType: "function", Action: "MODIFIED", BodyHash: "h" + id, LOC: 3,
		StartLine: 1, EndLine: 3, OccurredAt: at,
	}); err != nil {
		t.Fatalf("InsertEvent %s: %v", id, err)
	}
}

func sigSet(rows []SymbolHistoryRecord) map[string]int {
	m := make(map[string]int, len(rows))
	for _, r := range rows {
		m[r.Signature]++
	}
	return m
}

// TestSymbolHistory_SiblingSymbolsDoNotLeak is the bug: one symbol's history
// must not absorb siblings whose names merely extend it.
func TestSymbolHistory_SiblingSymbolsDoNotLeak(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UTC()

	seedSig(t, st, "sh-1", "f.go", "func:DoTask", now.Add(-3*time.Minute))
	seedSig(t, st, "sh-2", "f.go", "func:DoTask", now.Add(-2*time.Minute))
	seedSig(t, st, "sh-3", "f.go", "func:DoTaskAsync", now.Add(-time.Minute))
	seedSig(t, st, "sh-4", "f.go", "func:DoTaskHelper", now)

	got, err := st.SymbolHistory("f.go", "func:DoTask", 100)
	if err != nil {
		t.Fatalf("SymbolHistory: %v", err)
	}
	set := sigSet(got)
	if len(got) != 2 || set["func:DoTask"] != 2 {
		t.Errorf("SymbolHistory(f.go, func:DoTask) = %d rows %v, want exactly the 2 revisions of func:DoTask "+
			"(a longer sibling must not answer)", len(got), set)
	}
	for _, leaked := range []string{"func:DoTaskAsync", "func:DoTaskHelper"} {
		if set[leaked] != 0 {
			t.Errorf("SymbolHistory(f.go, func:DoTask) leaked %q: a sibling whose name merely extends the "+
				"needle matched an unanchored contains arm", leaked)
		}
	}
}

// TestSymbolHistory_MetacharacterSignaturesStillResolve is the CONTROL that
// caught the regression the fix introduced. '_' and '%' are LIKE wildcards; an
// equality arm binds its value raw (no ESCAPE processing there), and the LIKE
// arms take the escapeLike-quoted value. Binding escaped values on the equality
// arms made every snake_case signature match nothing.
func TestSymbolHistory_MetacharacterSignaturesStillResolve(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UTC()

	// Differ only by the metacharacter: '_' is a single-char wildcard, so an
	// unescaped one lets the sibling answer.
	seedSig(t, st, "mc-1", "f.go", "func:Do_Task", now)
	seedSig(t, st, "mc-2", "f.go", "func:Do-Task", now)
	seedSig(t, st, "mc-3", "g.go", "func:Do%Task", now)
	seedSig(t, st, "mc-4", "g.go", "func:DoXTask", now)

	for _, tc := range []struct {
		path, want string
	}{
		{"f.go", "func:Do_Task"},
		{"g.go", "func:Do%Task"},
	} {
		got, err := st.SymbolHistory(tc.path, tc.want, 100)
		if err != nil {
			t.Fatalf("SymbolHistory(%s, %s): %v", tc.path, tc.want, err)
		}
		set := sigSet(got)
		if len(got) != 1 || set[tc.want] != 1 {
			t.Errorf("SymbolHistory(%s, %s) = %d rows %v, want exactly that one symbol; a metacharacter "+
				"signature must resolve AND its sibling stay excluded", tc.path, tc.want, len(got), set)
		}
	}
}

// TestSymbolHistory_BareNameStillResolvesQualifiedSignature keeps the historical
// short-name spelling working: a bare name must still match the last component
// of a qualified signature, while stopping short of a longer sibling.
func TestSymbolHistory_BareNameStillResolvesQualifiedSignature(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UTC()

	seedSig(t, st, "bn-1", "h.go", "func:h.go::computeTotal", now)
	seedSig(t, st, "bn-2", "h.go", "func:h.go::computeTotals", now)

	got, err := st.SymbolHistory("h.go", "computeTotal", 100)
	if err != nil {
		t.Fatalf("SymbolHistory: %v", err)
	}
	set := sigSet(got)
	if len(got) != 1 || set["func:h.go::computeTotal"] != 1 {
		t.Errorf("SymbolHistory(h.go, computeTotal) = %d rows %v, want only func:h.go::computeTotal "+
			"(the '::' anchored arms must still resolve a bare name without matching computeTotals)", len(got), set)
	}
}

// TestSymbolHistory_SymbolWithoutSiblingIsUnaffected is the plain control: a
// symbol that has no prefix sibling resolves to exactly itself, before and after.
func TestSymbolHistory_SymbolWithoutSiblingIsUnaffected(t *testing.T) {
	st := openTestStore(t)
	now := time.Now().UTC()

	seedSig(t, st, "ct-1", "f.go", "func:UniqueThing", now)
	seedSig(t, st, "ct-2", "f.go", "func:AnotherUnique", now.Add(-time.Minute))

	for _, want := range []string{"func:UniqueThing", "func:AnotherUnique"} {
		got, err := st.SymbolHistory("f.go", want, 100)
		if err != nil {
			t.Fatalf("SymbolHistory(%s): %v", want, err)
		}
		set := sigSet(got)
		if len(got) != 1 || set[want] != 1 {
			t.Errorf("SymbolHistory(f.go, %s) = %d rows %v, want exactly itself", want, len(got), set)
		}
	}
}
