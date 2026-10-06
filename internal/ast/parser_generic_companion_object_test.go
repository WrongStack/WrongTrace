package ast

// Round 12: `companion` was missing from typeDeclModifiers, the allow-list
// typeDeclModifiersOnly uses to prove a type keyword sits in DECLARATION
// position (added in round 8 to stop a keyword inside a string literal from
// minting a phantom node).
//
// Round 11 added `object` to the keyword set, which made `object Registry {`
// visible but left Kotlin's NAMED singleton invisible: Kotlin spells it
// `companion object Factory`, so the fields before `object` are ["companion"],
// which the guard rejected. The whole declaration produced no node, so edits
// inside it emitted no Diff event and it carried no churn, thrashing or model
// attribution.
//
// Pre-fix probe FAIL: companion object Factory produced no node
// "class:Service.kt::Factory"; and editing its body produced no Diff event
// attributed to that signature. Post-fix: PASS, with the plain-`object`,
// two-word `enum class` and string-literal-guard controls all still green.

import (
	"strings"
	"testing"
)

// companionObjectFixture holds a named and an anonymous companion object.
// The Registry/Factory pair is what the Diff assertion edits.
const companionObjectFixture = `class Service {
    companion object {
        const val DEFAULT = 10
    }
    fun serve(): Int = 1
}

class Registry {
    companion object Factory {
        fun make(): Registry = Registry()
    }
}
`

// TestParseGenericSource_CompanionObjectIsVisible pins that a Kotlin
// `companion object Name` — the named singleton — produces a node.
func TestParseGenericSource_CompanionObjectIsVisible(t *testing.T) {
	snap := parseGenericSource("src/main/kotlin/Service.kt", []byte(companionObjectFixture), LangKotlin)
	for _, want := range []string{
		"class:Service.kt::Factory",
		"class:Service.kt::Registry",
		"class:Service.kt::Service",
		"function:Service.kt::make",
		"function:Service.kt::serve",
	} {
		if _, ok := snap.Nodes[want]; !ok {
			t.Errorf("missing node %q; got %v", want, sortedSigsOf(snap))
		}
	}
}

// TestParseGenericSource_CompanionObjectEditsEmitDiffEvents is the end-to-end
// consequence: an edit INSIDE the companion object must be attributed to that
// signature. Counting events alone would pass on the enclosing class node
// instead, so the signature is matched exactly.
func TestParseGenericSource_CompanionObjectEditsEmitDiffEvents(t *testing.T) {
	before := parseGenericSource("src/main/kotlin/Service.kt", []byte(companionObjectFixture), LangKotlin)

	edited := strings.Replace(companionObjectFixture,
		"        fun make(): Registry = Registry()",
		"        fun make(): Registry = Registry()\n        fun reset(): Registry = Registry()", 1)
	if edited == companionObjectFixture {
		t.Fatal("fixture edit did not apply")
	}
	after := parseGenericSource("src/main/kotlin/Service.kt", []byte(edited), LangKotlin)

	found := false
	for _, ev := range Diff("p", before, after).Events {
		if ev.Signature == "class:Service.kt::Factory" {
			found = true
			break
		}
	}
	if !found {
		var got []string
		for _, ev := range Diff("p", before, after).Events {
			got = append(got, ev.Signature)
		}
		t.Errorf("no Diff event attributed to class:Service.kt::Factory; got %v", got)
	}
}

// TestParseGenericSource_CompanionIsNotStolenFromNonDeclarations guards the
// two failure modes that make `object` risky: the round-8 positional guard
// must still reject a keyword inside a string literal, and `object` must not
// be read as a declaration keyword in ordinary Java variable position.
func TestParseGenericSource_CompanionIsNotStolenFromNonDeclarations(t *testing.T) {
	lit := parseGenericSource("src/main/java/Config.java", []byte(
		"class Config {\n    String load() {\n        return \"the class is not found\";\n    }\n}\n"), LangJava)
	if n, ok := lit.Nodes["class:Config.java::is"]; ok {
		t.Errorf("a `class` inside a string literal minted a phantom node: %+v", n)
	}

	java := parseGenericSource("src/main/java/Holder.java", []byte(
		"class Holder {\n    Object object = make();\n    Object make() {\n        return null;\n    }\n}\n"), LangJava)
	if _, ok := java.Nodes["class:Holder.java::="]; ok {
		t.Error("Java `Object object = ...` minted a phantom node named \"=\"")
	}
	if _, ok := java.Nodes["function:Holder.java::make"]; !ok {
		t.Errorf("Java method `make` lost its node; got %v", sortedSigsOf(java))
	}
}

// TestParseGenericSource_PlainObjectAndTwoWordTypesSurvive re-pins rounds 10
// and 11 so this round's map change cannot quietly undo them.
func TestParseGenericSource_PlainObjectAndTwoWordTypesSurvive(t *testing.T) {
	plain := parseGenericSource("a.kt", []byte("object Registry {\n    fun register() {}\n}\n"), LangKotlin)
	if _, ok := plain.Nodes["class:a.kt::Registry"]; !ok {
		t.Errorf("round 11 regression: plain `object Registry` lost its node; got %v", sortedSigsOf(plain))
	}

	enum := parseGenericSource("b.kt", []byte("enum class Color { RED, GREEN }\n"), LangKotlin)
	if _, ok := enum.Nodes["class:b.kt::Color"]; !ok {
		t.Errorf("round 10 regression: `enum class Color` no longer names Color; got %v", sortedSigsOf(enum))
	}
	if _, ok := enum.Nodes["class:b.kt::class"]; ok {
		t.Error("round 10 regression: the phantom node named `class` returned")
	}
}
