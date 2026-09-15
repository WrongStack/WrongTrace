package ast

import (
	"testing"
)

// TestTypedDeclName_RejectsStatementHeads pins that call expressions nested in
// statement heads never become declarations. The first typed-declaration cut
// took the first `name(` field after any other field, so each line below
// "declared" the call inside it (size, isValid, Some, compute, Foo, Open...)
// — and because generic-path signatures are keyed by name, that phantom node
// OVERWROTE the real declaration of the same name.
func TestTypedDeclName_RejectsStatementHeads(t *testing.T) {
	for _, line := range []string{
		`for (int i = 0; i < size(); i++) {`,
		`if (x != null && isValid(x)) {`,
		`match parse(input) {`,
		`if let Some(v) = map.get(&k) {`,
		`while let Some(x) = it.next() {`,
		`for x in items.iter() {`,
		`when (val r = compute()) {`,
		`return new Foo(bar) {`,
		`Runnable r = new Runnable() {`,
		`using (var s = Open()) {`,
		`lock (gate) {`,
		`synchronized (mutex) {`,
		`} else if (ready(x)) {`,
		`} catch (IOException e) {`,
		`throw new IllegalStateException(msg) {`,
		`await for (final e in stream()) {`,
		`on FormatException catch (e) {`,
		`list.forEach(x -> {`,
		`executor.submit(() -> {`,
		`x = compute(y) {`,
		`a < b(c) {`,
		`foo(bar) {`,
		`String s = String.format("%d", n) {`,
	} {
		if got := typedDeclName(line); got != "" {
			t.Errorf("typedDeclName(%q) = %q, want no declaration", line, got)
		}
	}
}

func TestTypedDeclName_AcceptsDeclarations(t *testing.T) {
	for line, want := range map[string]string{
		`public static void main(String[] args) {`:                  "main",
		`Map<String, List<Integer>> build(int n) {`:                 "build",
		`char *strdup(const char *s) {`:                             "strdup",
		`const std::string& name() const {`:                         "name",
		`void Foo::bar(int x) {`:                                    "Foo::bar",
		`@Override public void run() {`:                             "run",
		`Future<void> main() async {`:                               "main",
		`private async Task<int> LoadAsync(CancellationToken ct) {`: "LoadAsync",
		`int[] sorted(int[] xs) throws IOException {`:               "sorted",
		`public Foo(int x) {`:                                       "Foo",
	} {
		if got := typedDeclName(line); got != want {
			t.Errorf("typedDeclName(%q) = %q, want %q", line, got, want)
		}
	}
}

// TestParseGenericSource_CallInLoopHeadKeepsRealDeclaration is the end-to-end
// shape of the defect: the for-head call `size()` replaced the real `size`
// method (lines 2-4) with a node spanning the loop body.
func TestParseGenericSource_CallInLoopHeadKeepsRealDeclaration(t *testing.T) {
	src := "class L {\n  int size() {\n    return n;\n  }\n  void each() {\n    for (int i = 0; i < size(); i++) {\n      go(i);\n    }\n  }\n}\n"
	snap := parseGenericSource("x/Size.java", []byte(src), LangJava)
	n, ok := snap.Nodes["function:Size.java::size"]
	if !ok || n.StartLine != 2 || n.EndLine != 4 {
		t.Errorf("size node = %+v (present=%v), want lines 2-4", n, ok)
	}
	if e, ok := snap.Nodes["function:Size.java::each"]; !ok || e.StartLine != 5 || e.EndLine != 9 {
		t.Errorf("each node = %+v (present=%v), want lines 5-9", e, ok)
	}
}

// TestParseGenericSource_TypedDeclGatedByLanguage pins that the typed
// heuristic does not run for languages that never declare in that shape.
func TestParseGenericSource_TypedDeclGatedByLanguage(t *testing.T) {
	cases := []struct {
		path string
		lang Language
		src  string
	}{
		{"m.rs", LangRust, "fn run(input: &str) -> u32 {\n    match parse(input) {\n        Ok(v) => v,\n        Err(_) => 0,\n    }\n}\n"},
		{"m.rb", LangRuby, "def run\n  items.each_with_index(x) {\n  }\nend\n"},
		{"m.php", LangPHP, "function run() {\n  foreach ($xs as $x) {\n    Some($x) {\n  }\n}\n"},
	}
	for _, c := range cases {
		snap := parseGenericSource(c.path, []byte(c.src), c.lang)
		for sig := range snap.Nodes {
			switch sig {
			case "function:" + c.path + "::run":
			default:
				t.Errorf("%s: unexpected node %q", c.path, sig)
			}
		}
	}
}

// TestParseGenericSource_RustLifetimeDoesNotOpenString pins that a Rust
// lifetime quote is not a char-literal opener. `fn a(x: &'static str) {` used
// to open a "string" that never closed, so the brace count never returned to
// zero and a's body swallowed the rest of the file. (A generic lifetime list,
// `fn a<'a>(`, has the same lexer shape; the fixture avoids it only because
// the fn case keeps generics in the name.)
func TestParseGenericSource_RustLifetimeDoesNotOpenString(t *testing.T) {
	src := "fn a(x: &'static str) -> &'static str {\n    if x.is_empty() {\n        return x;\n    }\n    x\n}\nfn b() {\n    let open = '{';\n    let esc = '\\'';\n    let uni = '\\u{1F600}';\n}\nfn c() {\n}\n"
	snap := parseGenericSource("lib.rs", []byte(src), LangRust)
	for sig, lines := range map[string][2]uint32{
		"function:lib.rs::a": {1, 6},
		"function:lib.rs::b": {7, 11},
		"function:lib.rs::c": {12, 13},
	} {
		n, ok := snap.Nodes[sig]
		if !ok || n.StartLine != lines[0] || n.EndLine != lines[1] {
			t.Errorf("%s = %+v (present=%v), want lines %d-%d", sig, n, ok, lines[0], lines[1])
		}
	}
}

func BenchmarkTypedDeclName(b *testing.B) {
	lines := []string{
		`        for (int i = 0; i < size(); i++) {`,
		`    public static void main(String[] args) {`,
		`        return x + 1;`,
		`    }`,
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		for _, l := range lines {
			_ = typedDeclName(l[4:])
		}
	}
}
