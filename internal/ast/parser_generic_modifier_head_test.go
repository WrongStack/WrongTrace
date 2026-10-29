package ast

import (
	"strings"
	"testing"
)

// Round 24: a declaration head carrying a language-legal MODIFIER the generic
// recogniser did not expect produced no node at all, so the declaration emitted
// no Diff event, its body hash never moved and no model could be attributed to
// it. Two mechanisms, one root cause:
//
//   - the typed arm's plausibleTypeToken reused statementKeywords as a
//     type-token rejector, and Java's `synchronized` is BOTH a statement keyword
//     and a legal modifier, so `public synchronized void sync() {` was rejected
//     as a whole. The map's own comment claimed these words "can never appear in
//     the modifier/return-type prefix", which is false for exactly this one. The
//     same word in the head position was rejected by the fields[0] check;
//   - the Ruby arm matched only a bare `def ` prefix, so the ordinary
//     `private def foo` and `module_function def self.bar` spellings were
//     invisible.
//
// Pre-fix probe (62 heads across 8 languages, driven through ast.NewEngine ->
// Engine.Parse): "java synchronized method" and "ruby private def" both BLIND,
// with every control green; post-fix both are recognised.
func TestParseGenericSource_ModifierPrefixedHeadsAreRecognised(t *testing.T) {
	cases := []struct {
		name string
		file string
		lang Language
		src  string
		want string
	}{
		{"java: public synchronized method", "M.java", LangJava,
			"public synchronized void sync() {\n    work();\n}\n", "function:M.java::sync"},
		{"java: synchronized-first head", "M.java", LangJava,
			"synchronized void syncFirst() {\n    work();\n}\n", "function:M.java::syncFirst"},
		{"ruby: private def", "m.rb", LangRuby,
			"private def pd\n  1\nend\n", "function:m.rb::pd"},
		{"ruby: protected def", "m.rb", LangRuby,
			"protected def prot\n  1\nend\n", "function:m.rb::prot"},
		{"ruby: module_function def self.", "m.rb", LangRuby,
			"module_function def self.mf\n  1\nend\n", "function:m.rb::mf"},
		// Controls: the unqualified spellings keep working.
		{"control: java plain method", "M.java", LangJava,
			"public static void main(String[] a) {\n}\n", "function:M.java::main"},
		{"control: ruby bare def", "m.rb", LangRuby,
			"def bare\n  1\nend\n", "function:m.rb::bare"},
	}
	for _, tc := range cases {
		snap := parseGenericSource(tc.file, []byte(tc.src), tc.lang)
		if _, ok := snap.Nodes[tc.want]; !ok {
			t.Errorf("%s: missing node %q; got %v", tc.name, tc.want, sortedSigsOf(snap))
		}
	}
}

// TestParseGenericSource_ModifierRelaxationKeepsStatementHeadsOut is the
// negative control for the same change: accepting `synchronized` as a modifier
// must not turn a `synchronized (lock) { ... }` STATEMENT into a declaration.
// The discriminator is that the statement's next field opens with '('.
func TestParseGenericSource_ModifierRelaxationKeepsStatementHeadsOut(t *testing.T) {
	for _, src := range []string{
		"synchronized (lock) {\n    work();\n}\n",
		"public void f() {\n    synchronized (gate) {\n        work();\n    }\n}\n",
	} {
		snap := parseGenericSource("S.java", []byte(src), LangJava)
		for sig := range snap.Nodes {
			if strings.HasSuffix(sig, "::lock") || strings.HasSuffix(sig, "::gate") ||
				strings.HasSuffix(sig, "::synchronized") {
				t.Errorf("a synchronized statement head became the declaration %q (source %q)", sig, src)
			}
		}
	}
}
