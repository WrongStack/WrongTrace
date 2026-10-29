package ast

import (
	"testing"
)

// PHP declarations can share the `<?php` opener's line (`<?php class C {`,
// `<?php function f() {`). The opener tag is not a declaration modifier, so the
// head guards rejected the whole line and the declaration produced NO node — no
// body hash, no churn event, no model attribution — while the conventional form
// (opener on its own line) worked, which is why it went unnoticed.
//
// parseGenericSource now drops a leading `<?php` and a trailing `?>` for PHP
// before the dispatch switch, so both the type-declaration arm and the PHP
// `function` arm see the head they expect.
func TestParseGenericSource_PHPOpenerLineHeadsAreRecognised(t *testing.T) {
	cases := []struct {
		name string
		file string
		src  string
		want string
	}{
		{"opener + class", "a.php", "<?php class C {\n}\n", "class:a.php::C"},
		{"opener + trait", "b.php", "<?php trait T {\n}\n", "class:b.php::T"},
		{"opener + interface", "c.php", "<?php interface I {\n}\n", "class:c.php::I"},
		{"opener + function", "d.php", "<?php function f() {\n}\n", "function:d.php::f"},
		{"opener + class + closer on the same line", "e.php", "<?php class E { } ?>\n", "class:e.php::E"},
		// Controls: the conventional shapes keep working.
		{"opener on its own line", "f.php", "<?php\nclass F {\n}\n", "class:f.php::F"},
		{"no opener at all", "g.php", "class G {\n}\n", "class:g.php::G"},
	}
	for _, tc := range cases {
		snap := parseGenericSource(tc.file, []byte(tc.src), LangPHP)
		if _, ok := snap.Nodes[tc.want]; !ok {
			t.Errorf("%s: missing node %q; got %v", tc.name, tc.want, sortedSigsOf(snap))
		}
	}
}

// TestParseGenericSource_PHPOpenerLineWithoutDeclarationStaysSilent is the
// negative control: dropping the opener must not turn an ordinary PHP statement
// line into a declaration.
func TestParseGenericSource_PHPOpenerLineWithoutDeclarationStaysSilent(t *testing.T) {
	for _, tc := range []struct{ file, src string }{
		{"s.php", "<?php echo \"x\";\n"},
		{"s.php", "<?php $x = 1;\n"},
		{"s.php", "<?php new class {\n};\n"}, // an anonymous class is not a named declaration
	} {
		snap := parseGenericSource(tc.file, []byte(tc.src), LangPHP)
		if sigs := sortedSigsOf(snap); len(sigs) > 0 {
			t.Errorf("a non-declaration opener line produced nodes %v (source %q)", sigs, tc.src)
		}
	}
}
