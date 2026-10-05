package ast

import "testing"

// The line counts emitted by formatAddedDiff/formatDeletedDiff are NOT
// self-describing: a 0 means "this node genuinely has no body" in one case and
// "the snapshot's source was evicted, so the body is unavailable" in the other,
// and nothing on the wire distinguishes them.
//
// The eviction path is real and deliberate. cache.go's LRU byte budget
// (sourceBudgetBytes) sheds a cold snapshot's compressed source while KEEPING
// its node map, and native parsers leave Node.Body empty (parser.go:39 —
// "fallback body for generic parsers and synthetic tests"). nodeBody
// (diff.go) then resolves the byte range against an empty src, falls through to
// n.Body, and returns "". splitLines("") is nil, so the estimators return 0.
//
// cache.go documents the intended blast radius as "only the line-level
// diff_snippet degrades" — the counts were described as staying exact. They do
// not, which is what these tests pin.
//
// This is a CHARACTERIZATION test: it records the current, divergent behavior
// so it cannot silently worsen, and so any future fix is a deliberate,
// reviewable change rather than an accident. The fix is not mechanical — the
// wire format has no way to say "unknown" today (added_lines/deleted_lines are
// plain ints in Event), so resolving it is a schema decision, not a local edit.

const evictedProbeSrc = "package main\n\n" +
	"func DoTask() int {\n" +
	"\tx := 1\n" +
	"\ty := 2\n" +
	"\treturn x + y\n" +
	"}\n"

const evictedProbeSig = "func:DoTask() int"

// nativeNode builds a node the way the Go/JS/TS/Python tree-sitter parsers do:
// a byte range into the snapshot source, with Body left empty.
func nativeNode() Node {
	return Node{
		Signature: evictedProbeSig,
		Kind:      NodeFunction,
		Body:      "",
		StartByte: uint32(len("package main\n\n")),
		EndByte:   uint32(len(evictedProbeSrc)),
		StartLine: 3,
		EndLine:   8,
		Hash:      "h1",
		LOC:       5,
	}
}

// nextNode is the freshly-parsed counterpart: its byte range is computed
// against the NEW source, exactly as a real parser would emit it. Reusing
// nativeNode's stale range would make nodeBody slice a truncated prefix of
// the new body, which is not a state the producer can reach.
func nextNode() Node {
	newSrc := "package main\n\n" +
		"func DoTask() int {\n" +
		"\tx := 1\n" +
		"\ty := 2\n" +
		"\tz := 3\n" +
		"\treturn x + y + z\n" +
		"}\n"
	n := nativeNode()
	n.Hash = "h2"
	n.EndByte = uint32(len(newSrc))
	return n
}

func probeSnapshot(source string) *FileSnapshot {
	return &FileSnapshot{
		Path:       "f.go",
		Nodes:      map[string]Node{evictedProbeSig: nativeNode()},
		Hash:       "h1",
		RawContent: source,
	}
}

func deletedCounts(prev *FileSnapshot) (added, deleted int, ok bool) {
	for _, e := range Diff("repo", prev, nil).Events {
		if e.Action == ActionDeleted {
			return e.AddedLines, e.DeletedLines, true
		}
	}
	return 0, 0, false
}

// TestDeletedCountsCollapseToZeroWhenSourceIsEvicted is the discriminating
// case: the node map is retained in both snapshots, so the ONLY difference is
// whether the source survived the byte budget.
func TestDeletedCountsCollapseToZeroWhenSourceIsEvicted(t *testing.T) {
	warmAdd, warmDel, ok := deletedCounts(probeSnapshot(evictedProbeSrc))
	if !ok {
		t.Fatal("warm snapshot emitted no DELETED event")
	}
	if warmDel != 5 {
		t.Fatalf("warm deleted count = %d, want 5 (the node body is 5 lines)", warmDel)
	}

	coldAdd, coldDel, ok := deletedCounts(probeSnapshot(""))
	if !ok {
		t.Fatal("evicted snapshot emitted no DELETED event")
	}
	if coldAdd != 0 || coldDel != 0 {
		t.Fatalf("evicted deleted counts = +%d -%d, want +0 -0", coldAdd, coldDel)
	}

	// The divergence IS the finding: identical node, identical node map, and
	// the persisted counts differ purely because the source was shed.
	t.Logf("same node, node map retained: warm +%d -%d vs evicted +%d -%d",
		warmAdd, warmDel, coldAdd, coldDel)
}

// TestModifiedCountsDegradeToAllAdditions is the second divergence: with the
// previous source gone, generateLineDiff("", newBody) takes the
// len(oldLines)==0 arm and reports the ENTIRE new body as added, so a
// one-line edit to a cold file is persisted as a wholesale rewrite.
func TestModifiedCountsDegradeToAllAdditions(t *testing.T) {
	next := probeSnapshot("package main\n\n" +
		"func DoTask() int {\n" +
		"\tx := 1\n" +
		"\ty := 2\n" +
		"\tz := 3\n" +
		"\treturn x + y + z\n" +
		"}\n")
	next.Nodes[evictedProbeSig] = nextNode()
	next.Hash = "h2" // file-level hash must differ, or Diff short-circuits at diff.go:111

	cold := probeSnapshot("") // previous source evicted
	oldNode := nativeNode()
	oldNode.Hash = "h1"
	cold.Nodes[evictedProbeSig] = oldNode

	var found bool
	for _, e := range Diff("repo", cold, next).Events {
		if e.Action != ActionModified {
			continue
		}
		found = true
		// One line was actually inserted (z := 3 and the return change is
		// still 6 body lines, but the true delta is +1). Reporting the whole
		// body as added means added == the new body length.
		if e.AddedLines != 6 || e.DeletedLines != 0 {
			t.Fatalf("evicted-source MODIFIED counts = +%d -%d, want the degraded +6 -0", e.AddedLines, e.DeletedLines)
		}
		t.Logf("evicted-source MODIFIED persisted +%d -%d for a one-line edit (all-additions degradation)",
			e.AddedLines, e.DeletedLines)
	}
	if !found {
		t.Fatal("no MODIFIED event emitted for the evicted-source pair")
	}
}

// TestWarmCountsAreExact is the control: with the source present on BOTH
// sides, the counts are the true per-line delta, not a degraded bulk figure.
func TestWarmCountsAreExact(t *testing.T) {
	oldSrc := evictedProbeSrc
	newSrc := "package main\n\n" +
		"func DoTask() int {\n" +
		"\tx := 1\n" +
		"\ty := 2\n" +
		"\tz := 3\n" +
		"\treturn x + y + z\n" +
		"}\n"

	prev := probeSnapshot(oldSrc)
	next := probeSnapshot(newSrc)
	next.Nodes[evictedProbeSig] = nextNode()
	next.Hash = "h2" // file-level hash must differ, or Diff short-circuits at diff.go:111

	var found bool
	for _, e := range Diff("repo", prev, next).Events {
		if e.Action != ActionModified {
			continue
		}
		found = true
		// The true delta is one added statement plus a modified return line:
		// +2 -1. The warm path must report that exact figure, proving the
		// producer CAN count per-line when the source is available.
		if e.AddedLines != 2 || e.DeletedLines != 1 {
			t.Fatalf("warm MODIFIED counts = +%d -%d, want the exact +2 -1", e.AddedLines, e.DeletedLines)
		}
	}
	if !found {
		t.Fatal("no MODIFIED event emitted for the warm pair")
	}
}
