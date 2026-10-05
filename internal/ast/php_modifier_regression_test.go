package ast

import "testing"

func TestPHPFunctionModifierAdmission(t *testing.T) {
	e := newTestEngine(t)
	for _, head := range []string{"function", "public function", "private function", "protected function", "public static function", "private static function", "final protected function", "abstract protected function"} {
		snap := parseOrFatal(t, e, "fixture.php", "<?php\nclass Fixture {\n    "+head+" target() {\n        return 1;\n    }\n}\n")
		if _, ok := snap.Nodes["function:fixture.php::target"]; !ok {
			t.Fatalf("%s missing: %v", head, snap.SortedSignatures())
		}
	}
	for _, line := range []string{"$x = function ($y) { return $y; };", "static function ($y) { return $y; };", "return function ($y) { return $y; };", "public function"} {
		snap := parseOrFatal(t, e, "fixture.php", "<?php\n"+line+"\n")
		if len(snap.Nodes) != 0 {
			t.Fatalf("nondeclaration became node: %v", snap.SortedSignatures())
		}
	}
}
