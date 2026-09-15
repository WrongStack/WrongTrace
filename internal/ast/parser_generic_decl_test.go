package ast

import (
	"strings"
	"testing"
)

// TestParseGenericSource_KotlinFunAndTypedDecls pins round-4: parseGenericSource
// — the only path for .kt/.kts/.dart/.java/.cs/.c — extracts Kotlin `fun`
// declarations (modifiers included) and the typed `Type name(args) {` shape.
// Before this, supported.go's round-89 "reach the AST pipeline" contract held
// only for classes: a Kotlin file of top-level funs produced zero function
// nodes, so function edits never emitted Diff events. Pre-fix probe FAIL:
// kotlin functions=0; post-fix: 3 kotlin + 2 dart function nodes.
func TestParseGenericSource_KotlinFunAndTypedDecls(t *testing.T) {
	eng, err := NewEngine()
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	defer eng.Close()

	kotlin := `class Greeter(private val who: String) {
    fun greet(): String {
        return "hi $who"
    }
}

fun main() {
    println(Greeter("x").greet())
}

suspend fun fetchData(): Int {
    return 42
}
`
	snap, err := eng.Parse("src/main/kotlin/Demo.kt", []byte(kotlin))
	if err != nil || snap == nil {
		t.Fatalf("parse kotlin: snap=%v err=%v", snap != nil, err)
	}
	for _, want := range []string{
		"function:Demo.kt::greet",
		"function:Demo.kt::main",
		"function:Demo.kt::fetchData",
		"class:Demo.kt::Greeter",
	} {
		if _, ok := snap.Nodes[want]; !ok {
			t.Errorf("kotlin snapshot missing %q (have %d nodes)", want, len(snap.Nodes))
		}
	}

	dart := `void main() {
  print('hi');
}

class Counter {
  int inc(int v) {
    return v + 1;
  }
}
`
	dsnap, err := eng.Parse("lib/main.dart", []byte(dart))
	if err != nil || dsnap == nil {
		t.Fatalf("parse dart: snap=%v err=%v", dsnap != nil, err)
	}
	for _, want := range []string{
		"function:main.dart::main",
		"function:main.dart::inc",
		"class:main.dart::Counter",
	} {
		if _, ok := dsnap.Nodes[want]; !ok {
			t.Errorf("dart snapshot missing %q (have %d nodes)", want, len(dsnap.Nodes))
		}
	}

	// Statement heads and calls must not become declarations.
	for _, snap := range []*FileSnapshot{snap, dsnap} {
		for sig := range snap.Nodes {
			name := sig
			if idx := strings.LastIndex(sig, "::"); idx >= 0 {
				name = sig[idx+2:]
			}
			switch name {
			case "if", "for", "while", "switch", "catch", "return", "else", "do":
				t.Errorf("control-flow head leaked as declaration: %q", sig)
			}
		}
	}
}
