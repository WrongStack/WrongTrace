package ast

import (
	"strings"
	"testing"
)

func TestJSTSNestedDeclarationsKeepLexicalIdentity(t *testing.T) {
	e := newTestEngine(t)
	for _, path := range []string{"fixture.js", "fixture.ts"} {
		for _, fixture := range []struct{ source, prefix string }{
			{"function one() { function inner() { return 1; } return inner(); }\nfunction two() { function inner() { return 2; } return inner(); }\n", "function:"},
			{"const one = () => { const inner = () => 1; return inner(); };\nconst two = () => { const inner = () => 2; return inner(); };\n", "arrow_function:"},
		} {
			prev := parseOrFatal(t, e, path, fixture.source)
			for _, name := range []string{"one", "one.inner", "two", "two.inner"} {
				if _, ok := prev.Nodes[fixture.prefix+path+"::"+name]; !ok {
					t.Fatalf("missing %s in %v", name, prev.SortedSignatures())
				}
			}
			next := parseOrFatal(t, e, path, strings.Replace(fixture.source, "1", "9", 1))
			events := Diff("fixture", prev, next).Events
			if len(events) != 2 {
				t.Fatalf("expected owner+nested modifications: %+v", events)
			}
			for _, ev := range events {
				if ev.Action != ActionModified || strings.Contains(ev.Signature, "two") {
					t.Fatalf("wrong owner changed: %+v", ev)
				}
			}
		}
	}
}
