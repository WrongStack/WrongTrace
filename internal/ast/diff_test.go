package ast

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestDiff_AllTransitions(t *testing.T) {
	// 1. Both nil
	res := Diff("repo", nil, nil)
	if len(res.Events) != 0 {
		t.Errorf("expected 0 events for nil snapshots, got %d", len(res.Events))
	}

	snap1 := &FileSnapshot{
		Path: "main.go",
		Nodes: map[string]Node{
			"function:main.go::First": {
				Signature: "function:main.go::First",
				Kind:      NodeFunction,
				Body:      "func First() {\n\tprintln(1)\n}",
				StartLine: 1,
				EndLine:   3,
				LOC:       3,
				Hash:      "hash1",
			},
			"function:main.go::Second": {
				Signature: "function:main.go::Second",
				Kind:      NodeFunction,
				Body:      "func Second() {\n\tprintln(2)\n}",
				StartLine: 5,
				EndLine:   7,
				LOC:       3,
				Hash:      "hash2",
			},
		},
	}

	// 2. Added only (nil prev)
	resAdd := Diff("repo", nil, snap1)
	if len(resAdd.Events) != 2 {
		t.Fatalf("expected 2 added events, got %d", len(resAdd.Events))
	}
	for _, ev := range resAdd.Events {
		if ev.Action != ActionAdded {
			t.Errorf("expected action ADDED, got %s", ev.Action)
		}
		if ev.AddedLines != 3 || ev.DeletedLines != 0 {
			t.Errorf("unexpected added/deleted counts: +%d -%d", ev.AddedLines, ev.DeletedLines)
		}
		if !strings.Contains(ev.DiffSnippet, "+ ") {
			t.Errorf("expected + prefix in diff snippet: %s", ev.DiffSnippet)
		}
	}

	// 3. Deleted only (nil next)
	resDel := Diff("repo", snap1, nil)
	if len(resDel.Events) != 2 {
		t.Fatalf("expected 2 deleted events, got %d", len(resDel.Events))
	}
	for _, ev := range resDel.Events {
		if ev.Action != ActionDeleted {
			t.Errorf("expected action DELETED, got %s", ev.Action)
		}
		if ev.DeletedLines != 3 || ev.AddedLines != 0 {
			t.Errorf("unexpected added/deleted counts: +%d -%d", ev.AddedLines, ev.DeletedLines)
		}
		if !strings.Contains(ev.DiffSnippet, "- ") {
			t.Errorf("expected - prefix in diff snippet: %s", ev.DiffSnippet)
		}
	}

	// 4. Mixed: Modified + Deleted + Added
	snap2 := &FileSnapshot{
		Path: "main.go",
		Nodes: map[string]Node{
			"function:main.go::First": {
				Signature: "function:main.go::First",
				Kind:      NodeFunction,
				Body:      "func First() {\n\tprintln(100)\n\treturn\n}",
				StartLine: 1,
				EndLine:   4,
				LOC:       4,
				Hash:      "hash1_modified",
			},
			"function:main.go::Third": {
				Signature: "function:main.go::Third",
				Kind:      NodeFunction,
				Body:      "func Third() {}",
				StartLine: 9,
				EndLine:   9,
				LOC:       1,
				Hash:      "hash3",
			},
		},
	}

	resMixed := Diff("repo", snap1, snap2)
	// Order: DELETED (Second), MODIFIED (First), ADDED (Third)
	if len(resMixed.Events) != 3 {
		t.Fatalf("expected 3 mixed events, got %d", len(resMixed.Events))
	}
	if resMixed.Events[0].Action != ActionDeleted || resMixed.Events[0].Signature != "function:main.go::Second" {
		t.Errorf("event 0 should be DELETED Second: %+v", resMixed.Events[0])
	}
	if resMixed.Events[1].Action != ActionModified || resMixed.Events[1].Signature != "function:main.go::First" {
		t.Errorf("event 1 should be MODIFIED First: %+v", resMixed.Events[1])
	}
	if resMixed.Events[2].Action != ActionAdded || resMixed.Events[2].Signature != "function:main.go::Third" {
		t.Errorf("event 2 should be ADDED Third: %+v", resMixed.Events[2])
	}
}

func TestLineDiffGeneration(t *testing.T) {
	oldCode := `func Calculate() int {
    a := 1
    b := 2
    return a + b
}`
	newCode := `func Calculate() int {
    a := 10
    b := 2
    c := 3
    return a + b + c
}`

	diff, added, deleted := generateLineDiff(oldCode, newCode)
	if added == 0 || deleted == 0 {
		t.Errorf("expected added > 0 and deleted > 0, got +%d -%d", added, deleted)
	}
	if !strings.Contains(diff, "- ") || !strings.Contains(diff, "+ ") {
		t.Errorf("expected + and - in diff:\n%s", diff)
	}

	// Format helpers with empty body
	d1, a1, del1 := formatAddedDiff("")
	if d1 != "" || a1 != 0 || del1 != 0 {
		t.Error("formatAddedDiff on empty string failed")
	}
	d2, a2, del2 := formatDeletedDiff("")
	if d2 != "" || a2 != 0 || del2 != 0 {
		t.Error("formatDeletedDiff on empty string failed")
	}
}

func TestDiffSnippetIsBoundedAndKeepsCounts(t *testing.T) {
	line := strings.Repeat("x", 1024)
	body := strings.Repeat(line+"\n", 200)

	diff, added, deleted := formatAddedDiff(body)
	if added != 200 || deleted != 0 {
		t.Fatalf("counts = +%d -%d, want +200 -0", added, deleted)
	}
	if len(diff) > maxDiffSnippetBytes {
		t.Fatalf("diff snippet grew to %d bytes", len(diff))
	}
	if !strings.Contains(diff, "diff snippet truncated") {
		t.Fatal("bounded diff did not disclose truncation")
	}
}

func TestLineDiffOmitsDistantUnchangedContext(t *testing.T) {
	prefix := make([]string, 1000)
	for i := range prefix {
		prefix[i] = "unchanged"
	}
	oldCode := strings.Join(append(append([]string{}, prefix...), "old", "tail"), "\n")
	newCode := strings.Join(append(append([]string{}, prefix...), "new", "tail"), "\n")

	diff, added, deleted := generateLineDiff(oldCode, newCode)
	if added != 1 || deleted != 1 {
		t.Fatalf("counts = +%d -%d, want +1 -1", added, deleted)
	}
	contextLines := 0
	for _, line := range strings.Split(diff, "\n") {
		if strings.TrimSpace(line) == "unchanged" {
			contextLines++
		}
	}
	if contextLines > diffContextLines {
		t.Fatalf("diff retained distant unchanged context:\n%s", diff)
	}
	if !strings.Contains(diff, "unchanged lines omitted") {
		t.Fatal("diff did not mark omitted context")
	}
}

// longLineDecls builds `count` shared lines of roughly `width` bytes each,
// followed by a line that differs between the two revisions. It models an
// ordinary edit made below a block of generated/base64-inflated source, which
// is what a minified or machine-written .js file looks like in practice.
func longLineDecls(count, width int, tail string) string {
	var b strings.Builder
	for i := 0; i < count; i++ {
		b.WriteString("var s")
		b.WriteString(strconv.Itoa(i))
		b.WriteString(` = "`)
		b.WriteString(strings.Repeat("x", width))
		b.WriteString(`";`)
		b.WriteByte('\n')
	}
	b.WriteString(tail)
	return b.String()
}

// Regression guard: generateLineDiff used to initialise the builder's byte cap
// AFTER emitting the common-prefix context, so those writes were uncapped. A
// long shared prefix pushed the builder past maxDiffSnippetBytes and the
// capacity arithmetic below it (maxDiffSnippetBytes - b.Len()) then went
// negative, panicking inside strings.Builder.Grow. The cap is now decided
// before the first emit, so the snippet stays bounded and the arithmetic
// cannot underflow.
func TestGenerateLineDiffLongCommonPrefixStaysBounded(t *testing.T) {
	// 3 shared lines x ~25 KiB overflows the 64 KiB cap by a wide margin.
	oldText := longLineDecls(3, 25000, "var n = 1;\n")
	newText := longLineDecls(3, 25000, "var n = 2;\n")

	diff, _, _ := generateLineDiff(oldText, newText)
	if len(diff) > maxDiffSnippetBytes {
		t.Fatalf("diff snippet grew to %d bytes, cap is %d", len(diff), maxDiffSnippetBytes)
	}
	if !strings.Contains(diff, "diff snippet truncated") {
		t.Error("over-budget diff did not disclose truncation")
	}
}

// The same overflow is reachable through the exported entry point the watcher
// drives, at both file level and per-node level.
func TestDiffLongSharedPrefixDoesNotPanic(t *testing.T) {
	t.Run("file level", func(t *testing.T) {
		prev := &FileSnapshot{Path: "app.js", RawContent: longLineDecls(3, 25000, "var n = 1;\n")}
		next := &FileSnapshot{Path: "app.js", RawContent: longLineDecls(3, 25000, "var n = 2;\n")}

		res := Diff("repo", prev, next)
		if len(res.FileDiff) > maxDiffSnippetBytes {
			t.Fatalf("FileDiff grew to %d bytes, cap is %d", len(res.FileDiff), maxDiffSnippetBytes)
		}
	})

	t.Run("modified node", func(t *testing.T) {
		sig := "function:app.js::build"
		bodyA := longLineDecls(3, 25000, "return 1;\n")
		bodyB := longLineDecls(3, 25000, "return 2;\n")
		prev := &FileSnapshot{Path: "app.js", RawContent: bodyA, Nodes: map[string]Node{
			sig: {Signature: sig, Kind: NodeFunction, Body: bodyA, Hash: "hash-a"},
		}}
		next := &FileSnapshot{Path: "app.js", RawContent: bodyB, Nodes: map[string]Node{
			sig: {Signature: sig, Kind: NodeFunction, Body: bodyB, Hash: "hash-b"},
		}}

		res := Diff("repo", prev, next)
		if len(res.Events) != 1 {
			t.Fatalf("expected 1 MODIFIED event, got %d", len(res.Events))
		}
		if res.Events[0].Action != ActionModified {
			t.Errorf("action = %s, want %s", res.Events[0].Action, ActionModified)
		}
		if len(res.Events[0].DiffSnippet) > maxDiffSnippetBytes {
			t.Fatalf("DiffSnippet grew to %d bytes, cap is %d",
				len(res.Events[0].DiffSnippet), maxDiffSnippetBytes)
		}
	})
}

// Boundary half of the contract: a prefix that fits under the cap must still be
// emitted. Without this case the panic could also be "fixed" by dropping all
// context, which would silently degrade every near-cap snippet.
func TestGenerateLineDiffKeepsContextUnderTheCap(t *testing.T) {
	// 2 shared lines x 20 KiB ≈ 40 KiB: comfortably under the 64 KiB cap.
	oldText := longLineDecls(2, 20000, "var n = 1;\n")
	newText := longLineDecls(2, 20000, "var n = 2;\n")

	diff, added, deleted := generateLineDiff(oldText, newText)
	if len(diff) > maxDiffSnippetBytes {
		t.Fatalf("diff snippet grew to %d bytes", len(diff))
	}
	if !strings.Contains(diff, "var s0") {
		t.Error("legitimate near-cap context was dropped")
	}
	if strings.Contains(diff, "diff snippet truncated") {
		t.Error("under-budget diff was truncated")
	}
	if added != 1 || deleted != 1 {
		t.Errorf("counts = +%d -%d, want +1 -1", added, deleted)
	}
}

// End-to-end witness through the real Tree-sitter JavaScript parser, so the
// guard cannot drift away from what Engine.HandleFileChange actually feeds Diff.
func TestDiffRealParserLongLineFileStaysBounded(t *testing.T) {
	e, err := NewEngine()
	if err != nil {
		t.Skipf("tree-sitter engine unavailable: %v", err)
	}
	defer e.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "gen.min.js")

	before, err := e.Parse(path, []byte(longLineDecls(3, 25000, "var n = 1;\n")))
	if err != nil || before == nil {
		t.Skipf("javascript fixture did not parse: err=%v nil=%v", err, before == nil)
	}
	e.SetSnapshot(before)

	after, err := e.Parse(path, []byte(longLineDecls(3, 25000, "var n = 2;\n")))
	if err != nil || after == nil {
		t.Fatalf("edited javascript fixture did not parse: err=%v nil=%v", err, after == nil)
	}

	prev, ok := e.Snapshot(path)
	if !ok || prev == nil {
		t.Fatal("no cached snapshot for the parsed file")
	}

	res := Diff("repo", prev, after)
	if len(res.FileDiff) > maxDiffSnippetBytes {
		t.Fatalf("FileDiff grew to %d bytes, cap is %d", len(res.FileDiff), maxDiffSnippetBytes)
	}
}
