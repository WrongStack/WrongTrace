package ast

// Regression (round 11): typeDeclName recognised class/interface/enum but not
// `object`, so a Kotlin singleton declaration produced NO node. The declaration
// was absent from the snapshot, so an edit inside it matched no signature and
// emitted no ADDED/MODIFIED/DELETED Diff event — no churn analytics, no
// thrashing/fragility signal, no model attribution. Round 10's probe observed it
// directly: `object Registry { fun register(...) }` yielded only the inner
// function node.
//
// Round 6 DELIBERATELY EXCLUDED `object` for a sound reason: it is not a
// reserved word in Java, so `Object object = get();` would have entered the arm
// and taken the following token as a type name. Round 8 added the
// typeDeclModifiersOnly POSITION guard that removes exactly that hazard — the
// field before `object` in that Java line is `Object`, not a modifier, so the
// line is declined. Round 11 completes the earlier deferral; the controls below
// prove each previously-dangerous shape stays clean.
//
// `companion object` is NOT covered here: its name position differs from a bare
// `object X` and needs its own handling.

import (
	"testing"
)

// TestParseGenericSource_KotlinObjectIsVisible pins that a Kotlin singleton is a
// node and that editing its body emits a Diff event.
func TestParseGenericSource_KotlinObjectIsVisible(t *testing.T) {
	eng, err := NewEngine()
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	defer eng.Close()

	before, err := eng.Parse("R.kt",
		[]byte("object Registry {\n    fun register(n: String) {\n    }\n}\n"))
	if err != nil || before == nil {
		t.Fatalf("parse before: %v", err)
	}
	if _, ok := before.Nodes["class:R.kt::Registry"]; !ok {
		t.Fatalf("`object Registry` produced no node; got %v", sortedSigsOf(before))
	}

	after, err := eng.Parse("R.kt",
		[]byte("object Registry {\n    fun register(n: String) {\n    }\n\n    fun unregister(n: String) {\n    }\n}\n"))
	if err != nil || after == nil {
		t.Fatalf("parse after: %v", err)
	}
	if events := Diff("p", before, after).Events; len(events) == 0 {
		t.Errorf("adding a function to a Kotlin `object` emitted 0 Diff events; got %v",
			sortedSigsOf(after))
	}
}

// TestParseGenericSource_ObjectDoesNotMisFire is the control for the hazard
// round 6 cited: `object` is an ordinary identifier in Java, and Kotlin's
// anonymous `object : Interface` expression is not a named type.
func TestParseGenericSource_ObjectDoesNotMisFire(t *testing.T) {
	// Java: `Object object = get();` is the exact line that got `object` excluded.
	java := "class Holder {\n    Object object = get();\n\n    private Object pick() {\n        return null;\n    }\n}\n"
	snap := parseGenericSource("src/main/java/Holder.java", []byte(java), LangJava)
	got := sortedSigsOf(snap)
	for _, s := range got {
		if s != "class:Holder.java::Holder" && s != "function:Holder.java::pick" {
			t.Errorf("`object` mis-fired in Java, minting %q; got %v", s, got)
		}
	}

	// Kotlin: an anonymous object expression is not a named declaration.
	anon := "val r = object : Runnable {\n    override fun run() {\n    }\n}\n"
	asnap := parseGenericSource("A.kt", []byte(anon), LangKotlin)
	agot := sortedSigsOf(asnap)
	for _, s := range agot {
		if s != "function:A.kt::run" {
			t.Errorf("an anonymous `object : Runnable` expression minted %q; got %v", s, agot)
		}
	}
}

// TestParseGenericSource_ObjectLeavesPriorCoverageIntact pins that rounds 1, 6,
// 8, 9 and 10 are untouched by adding this keyword.
func TestParseGenericSource_ObjectLeavesPriorCoverageIntact(t *testing.T) {
	for _, c := range []struct {
		path, src string
		lang      Language
		want      string
	}{
		{"S.kt", "class Repo {\n    fun find(id: String): String {\n        return id\n    }\n}\n",
			LangKotlin, "class:S.kt::Repo"},
		{"E.kt", "enum class Color {\n    RED\n}\n", LangKotlin, "class:E.kt::Color"},
		{"K.kt", "fun interface Sam {\n    fun call()\n}\n", LangKotlin, "class:K.kt::Sam"},
		{"D.kt", "data class User(val id: Int) {\n}\n", LangKotlin, "class:D.kt::User"},
		{"J.java", "class J {\n    void m() {\n        log(\"the class is fine\");\n    }\n}\n",
			LangJava, "class:J.java::J"},
	} {
		snap := parseGenericSource(c.path, []byte(c.src), c.lang)
		if _, ok := snap.Nodes[c.want]; !ok {
			t.Errorf("%s: missing %q; got %v", c.path, c.want, sortedSigsOf(snap))
		}
	}
}
