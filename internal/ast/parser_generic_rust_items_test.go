package ast

// Regression (round 7): rustDecl accepted ONLY the `fn` and `struct` keywords.
// Rust's other item declarations — `trait`, `impl`, `union`, `mod` — matched no
// arm, so they produced NO node. The declaration was absent from the snapshot, so
// an edit anywhere inside it matched no signature and emitted no ADDED/MODIFIED/
// DELETED Diff event: no churn analytics, no thrashing/fragility signal, no model
// attribution for symbols defined in a trait or impl block.
//
// This is a SEPARATE gap from the qualifier one rounds 4/10 fixed. Those made
// `fn`/`struct` visible THROUGH a modifier prefix (`pub(crate) async fn`); these
// four are new KEYWORDS the classifier never accepted.
//
// `enum` is deliberately NOT asserted for rustDecl: round 6 added it to the
// shared type-declaration arm, which is evaluated BEFORE `case lang == LangRust`,
// so Rust `enum` already resolves there. Asserting it here would pass for a
// reason that has nothing to do with rustDecl.
//
// Widening is safe because all four are RESERVED Rust keywords — no identifier
// can shadow one — and because the statement heads this classifier must not claim
// (`match x {`, `if let Some(v) = f() {`, `loop {`) still fall through. The
// controls below pin both, along with the earlier rounds' coverage.

import (
	"testing"
)

// TestParseRust_TraitImplUnionModAreVisible pins that each Rust item keyword
// produces a node, and that editing a trait body emits a Diff event.
func TestParseRust_TraitImplUnionModAreVisible(t *testing.T) {
	eng, err := NewEngine()
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	defer eng.Close()

	src := "pub trait Greeter {\n    fn hi(&self);\n}\n\n" +
		"impl Greeter {\n    fn hi(&self) {\n    }\n}\n\n" +
		"union Payload {\n    a: u32,\n}\n\n" +
		"mod util;\n\n" +
		"pub(crate) mod hidden;\n\n" +
		"impl<T> Wrapper<T> {\n    fn get(&self) {\n    }\n}\n"

	snap, err := eng.Parse("lib.rs", []byte(src))
	if err != nil || snap == nil {
		t.Fatalf("parse: snap=%v err=%v", snap != nil, err)
	}
	for _, want := range []string{
		"class:lib.rs::Greeter", // trait
		"class:lib.rs::Payload", // union
		"class:lib.rs::util",    // mod
		"class:lib.rs::hidden",  // qualified mod
		"class:lib.rs::Wrapper", // impl<T> — generics on the keyword
		"function:lib.rs::hi",   // method inside the impl block
	} {
		if _, ok := snap.Nodes[want]; !ok {
			t.Errorf("missing node %q; got %v", want, rustNodeSigs(snap))
		}
	}

	// The end-to-end contract: adding a method to a trait must emit an event.
	before, err := eng.Parse("t.rs", []byte("pub trait Greeter {\n    fn hi(&self);\n}\n"))
	if err != nil {
		t.Fatalf("parse before: %v", err)
	}
	after, err := eng.Parse("t.rs",
		[]byte("pub trait Greeter {\n    fn hi(&self);\n    fn bye(&self);\n}\n"))
	if err != nil {
		t.Fatalf("parse after: %v", err)
	}
	if events := Diff("p", before, after).Events; len(events) == 0 {
		t.Error("adding a method to a Rust trait emitted 0 Diff events, want > 0")
	}
}

// TestParseRust_ItemKeywordsAreNotStolenFromNonDeclarations is the control. Rust
// statement heads share the `Type name(args) {` shape, which is exactly why
// typedDeclLanguage excludes Rust; widening rustDecl must not let them in.
func TestParseRust_ItemKeywordsAreNotStolenFromNonDeclarations(t *testing.T) {
	src := "fn a(x: &str) -> u32 {\n" +
		"    match parse(x) {\n        Ok(v) => v,\n        Err(_) => 0,\n    }\n" +
		"    if let Some(v) = m.get(1) {\n        let _ = v;\n    }\n" +
		"    loop {\n    }\n" +
		"    while let Some(x) = it.next() {\n        let _ = x;\n    }\n" +
		"}\n"
	snap := parseGenericSource("lib.rs", []byte(src), LangRust)
	got := rustNodeSigs(snap)
	if len(got) != 1 || got[0] != "function:lib.rs::a" {
		t.Errorf("statement heads leaked declarations: got %v, want exactly [function:lib.rs::a]", got)
	}
}

// TestParseRust_PreviousRoundsCoverageSurvives pins that the earlier rounds'
// keyword and qualifier fixes are untouched by this one.
func TestParseRust_PreviousRoundsCoverageSurvives(t *testing.T) {
	// Round 4/10: qualified fn and struct.
	src := "pub(crate) async fn worker() {\n}\n\npub(super) struct Hidden {\n    a: u32,\n}\n"
	snap := parseGenericSource("lib.rs", []byte(src), LangRust)
	got := rustNodeSigs(snap)
	for _, want := range []string{"function:lib.rs::worker", "struct:lib.rs::Hidden"} {
		found := false
		for _, g := range got {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Errorf("round 4/10 coverage regressed: missing %q; got %v", want, got)
		}
	}

	// Round 6: enum resolves in the SHARED arm, ahead of rustDecl.
	en := parseGenericSource("e.rs", []byte("enum Color {\n    Red,\n}\n"), LangRust)
	if _, ok := en.Nodes["class:e.rs::Color"]; !ok {
		t.Errorf("round 6 enum coverage regressed; got %v", rustNodeSigs(en))
	}
}

func rustNodeSigs(s *FileSnapshot) []string {
	out := make([]string, 0, len(s.Nodes))
	for sig := range s.Nodes {
		out = append(out, sig)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
