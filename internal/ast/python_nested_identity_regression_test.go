package ast

import (
	"strings"
	"testing"
)

func TestPythonNestedFunctionsKeepLexicalIdentity(t *testing.T) {
	e := newTestEngine(t)
	src := "def one():\n    def inner():\n        return 1\n    return inner()\ndef two():\n    def inner():\n        return 2\n    return inner()\n"
	prev := parseOrFatal(t, e, "fixture.py", src)
	for _, name := range []string{"one", "one.inner", "two", "two.inner"} {
		if _, ok := prev.Nodes["function:fixture.py::"+name]; !ok {
			t.Fatalf("missing %s: %v", name, prev.SortedSignatures())
		}
	}
	next := parseOrFatal(t, e, "fixture.py", strings.Replace(src, "return 1", "return 9", 1))
	events := Diff("fixture", prev, next).Events
	if len(events) != 2 {
		t.Fatalf("want two modifications: %+v", events)
	}
	for _, ev := range events {
		if ev.Action != ActionModified || strings.Contains(ev.Signature, "two") {
			t.Fatalf("wrong owner changed: %+v", ev)
		}
	}
}
