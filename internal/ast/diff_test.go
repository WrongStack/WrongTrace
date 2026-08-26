package ast

import (
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
