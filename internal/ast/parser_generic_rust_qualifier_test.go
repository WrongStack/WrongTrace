package ast

// Regression (r4-rustdecl): parseGenericSource had no LangRust case, and its
// generic arms matched only the literal heads "pub fn "/"fn " and
// "pub struct "/"struct ". Every Rust declaration carrying a visibility or
// modifier token — `pub(crate)`, `async`, `unsafe`, `extern "C"`, `const` —
// therefore produced NO node, and an edit to it emitted no ADDED/MODIFIED/
// DELETED event. Reachable because `.rs` maps to LangRust and Rust has no
// compiled Tree-sitter grammar, so .rs files always land in parseGenericSource.
// rustDecl now strips those prefixes, accepting only the fn/struct keywords so
// Rust statement heads (`match x {`, `if let Some(v) = f() {`) stay invisible —
// the reason typedDeclLanguage excludes Rust.

import (
	"sort"
	"testing"
)

const rustQualifierFixture = `pub(crate) async fn crate_vis_async() {
    let x = 1;
}

async fn worker() {
    let y = 2;
}

unsafe fn danger() {
    let z = 3;
}

const fn konst() -> u32 {
    1
}

extern "C" fn ffi() {}

pub(in crate::inner) fn restricted() {}

pub(crate) struct Hidden {
    a: u32,
}

pub(super) struct Sibling {
    b: u32,
}

fn plain() {}

pub fn exported() {}

struct Plain {
    c: u32,
}

fn caller() {
    if let Some(v) = lookup(1) {
        consume(v);
    }
}
`

func TestParseRust_QualifiedDeclarationsAreVisible(t *testing.T) {
	eng, err := NewEngine()
	if err != nil {
		t.Fatalf("ast engine: %v", err)
	}
	defer eng.Close()

	snap, err := eng.Parse("lib.rs", []byte(rustQualifierFixture))
	if err != nil {
		t.Fatalf("parse lib.rs: %v", err)
	}
	if snap == nil {
		t.Fatal("Parse returned nil for .rs; the Rust generic path is not routed")
	}

	want := map[string]bool{
		"function:lib.rs::crate_vis_async": true, // pub(crate) async fn
		"function:lib.rs::worker":          true, // async fn
		"function:lib.rs::danger":          true, // unsafe fn
		"function:lib.rs::konst":           true, // const fn
		"function:lib.rs::ffi":             true, // extern "C" fn
		"function:lib.rs::restricted":      true, // pub(in crate::inner) fn
		"struct:lib.rs::Hidden":            true, // pub(crate) struct
		"struct:lib.rs::Sibling":           true, // pub(super) struct
		// Controls: the literal heads that already worked.
		"function:lib.rs::plain":    true,
		"function:lib.rs::exported": true,
		"struct:lib.rs::Plain":      true,
		"function:lib.rs::caller":   true,
	}

	var got []string
	for sig := range snap.Nodes {
		got = append(got, sig)
	}
	sort.Strings(got)

	for sig := range want {
		if _, ok := snap.Nodes[sig]; !ok {
			t.Errorf("qualified declaration missing: %s (have %v)", sig, got)
		}
	}
	// False-positive guard: statement heads must never be declared.
	for _, bad := range []string{"function:lib.rs::lookup", "function:lib.rs::consume", "function:lib.rs::let"} {
		if _, ok := snap.Nodes[bad]; ok {
			t.Errorf("statement head %s was declared as a node", bad)
		}
	}
	if len(got) != len(want) {
		t.Errorf("node set = %v, want exactly the %d declared symbols", got, len(want))
	}
}

// TestParseRust_QualifierIsNotStolenFromNonDeclarations pins the narrowness of
// the new arm: qualifier tokens alone must not create nodes, and a plain
// identifier that merely starts with a qualifier ("pubkey") is never stripped.
func TestParseRust_QualifierIsNotStolenFromNonDeclarations(t *testing.T) {
	src := `fn tally() {
    let pubkey = 1;
    unsafe {
        let async_read = pubkey;
    }
    match async_read {
        _ => {}
    }
}
`
	eng, err := NewEngine()
	if err != nil {
		t.Fatalf("ast engine: %v", err)
	}
	defer eng.Close()

	snap, err := eng.Parse("t.rs", []byte(src))
	if err != nil {
		t.Fatalf("parse t.rs: %v", err)
	}
	if _, ok := snap.Nodes["function:t.rs::tally"]; !ok {
		t.Fatalf("control declaration missing; nodes = %v", snap.Nodes)
	}
	if len(snap.Nodes) != 1 {
		t.Errorf("expected exactly the one real declaration, got %d: %v", len(snap.Nodes), snap.Nodes)
	}
}
