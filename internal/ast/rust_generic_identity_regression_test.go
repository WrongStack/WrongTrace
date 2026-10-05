package ast

import "testing"

func TestRustGenericFunctionIdentity(t *testing.T) {
	e := newTestEngine(t)
	for _, head := range []string{"fn", "pub fn", "pub(crate) async fn"} {
		prev := parseOrFatal(t, e, "fixture.rs", head+" identity<T>(x: T) -> T {\n    x\n}\n")
		next := parseOrFatal(t, e, "fixture.rs", head+" identity<U>(x: U) -> U {\n    x\n}\n")
		events := Diff("fixture", prev, next).Events
		if len(events) != 1 || events[0].Action != ActionModified || events[0].Signature != "function:fixture.rs::identity" {
			t.Fatalf("generic identity churn: %+v", events)
		}
	}
}
