package ast

import (
	"sort"
	"strings"
	"testing"
)

// TestParseGenericSource_KotlinGenericFunctions pins that Kotlin generic
// functions are declarations under their real names. The type-parameter list
// is skipped as one balanced group whether it is glued to the name
// (`fun <T>identity(`), spaced (`fun <T> spaced(`), spans fields
// (`fun <T, U>process(`) or nests (`fun <T: Comparable<T>>compare(`); an
// extension receiver is kept (`List<T>.second`) exactly as non-generic
// extensions have always been named.
//
// History: the first cut returned "<T>greet" as the name; the follow-up
// rejected every generic fun outright, which made the glued form invisible
// while the spaced form fell through to the typed-declaration heuristic.
// `fun interface Callback {` is a SAM interface, not a function named
// "interface".
func TestParseGenericSource_KotlinGenericFunctions(t *testing.T) {
	eng, err := NewEngine()
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	defer eng.Close()

	// Fixture names are distinct on purpose: generic-path signatures carry no
	// enclosing class (a known limitation), so a top-level fun sharing a
	// method's name would collapse onto one node.
	src := `class Greeter(private val who: String) {
    fun greet(): String = "hi $who"
}

fun <T>identity(x: T): T = x
fun <T> spaced(): T = TODO()
fun <T, U>process(a: T, b: U) = a
fun <R : Any>get(): R = TODO()
fun <T: Comparable<T>>compare(other: T): Int = 0
inline fun <reified T> List<T>.second(): T = this[1]

fun interface Callback {
    fun onEvent(x: Int)
}

fun main() {
    for (i in 0 until size()) {
        println(Greeter("x").greet())
    }
}
`

	snap, err := eng.Parse("src/main/kotlin/Demo.kt", []byte(src))
	if err != nil || snap == nil {
		t.Fatalf("parse kotlin: snap=%v err=%v", snap != nil, err)
	}

	want := []string{
		"class:Demo.kt::Callback",
		"class:Demo.kt::Greeter",
		"function:Demo.kt::List<T>.second",
		"function:Demo.kt::compare",
		"function:Demo.kt::get",
		"function:Demo.kt::greet",
		"function:Demo.kt::identity",
		"function:Demo.kt::main",
		"function:Demo.kt::onEvent",
		"function:Demo.kt::process",
		"function:Demo.kt::spaced",
	}
	got := nodeKeys(snap.Nodes)
	sort.Strings(got)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("kotlin nodes mismatch\n got: %v\nwant: %v", got, want)
	}

	if n, ok := snap.Nodes["function:Demo.kt::main"]; !ok || n.StartLine != 16 || n.EndLine != 20 {
		t.Errorf("main body = %+v, want lines 16-20 (the for head must not start a node)", n)
	}
}

func nodeKeys(nodes map[string]Node) []string {
	keys := make([]string, 0, len(nodes))
	for k := range nodes {
		keys = append(keys, k)
	}
	return keys
}
