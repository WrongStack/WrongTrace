package ast

// Regression (round 9): typedDeclName consulted statementKeywords TWICE — once
// against the line's HEAD field (correct) and once against the DECLARED NAME
// (wrong). The map is deliberately overloaded with statement heads from Rust,
// Python, Swift, Kotlin and JS, and many of its entries are ordinary, legal
// method names in Java/C#/C++/Dart — the languages typedDeclLanguage gates on.
//
// `is`, `on`, `with`, `from`, `select`, `where`, `delete`, `in` and `lock` are
// everyday Java method names. Each one made typedDeclName return "", dropping a
// REAL declaration from the snapshot; editing it then emitted no
// ADDED/MODIFIED/DELETED Diff event, so the symbol contributed no churn, no
// thrashing/fragility signal and no model attribution — the same consequence as
// the rounds 1/6/7/8 coverage gaps, reached from the opposite direction.
//
// The guard that actually rejects statement heads is the HEAD check, since a
// statement head always STARTS the line, together with the `j == 0` "call, not a
// declaration" rule and plausibleTypeToken. The control below pins that
// removing the name-side filter did not let a single statement head through.

import (
	"testing"
)

// TestTypedDeclName_LegalJavaMethodNamesAreNotRejected is the bug: a method whose
// name collides with another language's statement keyword is still a declaration.
func TestTypedDeclName_LegalJavaMethodNamesAreNotRejected(t *testing.T) {
	src := "class Config {\n" +
		"    boolean is() {\n        return true;\n    }\n" +
		"    void lock() {\n    }\n" +
		"    void on(int n) {\n    }\n" +
		"    Config with(String k) {\n        return this;\n    }\n" +
		"    static Config from(String s) {\n        return null;\n    }\n" +
		"    void select() {\n    }\n" +
		"    void delete() {\n    }\n" +
		"    void where() {\n    }\n" +
		"}\n"
	snap := parseGenericSource("src/main/java/Config.java", []byte(src), LangJava)
	for _, want := range []string{
		"function:Config.java::is",
		"function:Config.java::lock",
		"function:Config.java::on",
		"function:Config.java::with",
		"function:Config.java::from",
		"function:Config.java::select",
		"function:Config.java::delete",
		"function:Config.java::where",
	} {
		if _, ok := snap.Nodes[want]; !ok {
			t.Errorf("real Java method %q produced no node: its name collides with statementKeywords. "+
				"Got %v", want, sortedSigsOf(snap))
		}
	}
}

// TestTypedDeclName_StatementHeadsAreStillRejected is the control for this fix:
// dropping the name-side keyword filter must not admit a single statement head.
// A statement head always begins the line, so the head-side check covers these.
func TestTypedDeclName_StatementHeadsAreStillRejected(t *testing.T) {
	for _, line := range []string{
		`for (int i = 0; i < size(); i++) {`,
		`if (x != null && isValid(x)) {`,
		`match parse(input) {`,
		`if let Some(v) = map.get(&k) {`,
		`while let Some(x) = it.next() {`,
		`when (val r = compute()) {`,
		`return new Foo(bar) {`,
		`Runnable r = new Runnable() {`,
		`using (var s = Open()) {`,
		`lock (gate) {`,
		`synchronized (mutex) {`,
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
			t.Errorf("typedDeclName(%q) = %q, want no declaration — the name-side keyword filter was "+
				"removed but this is a statement head, not a declaration", line, got)
		}
	}
}

// TestTypedDeclName_KeepsRejectingRealDeclarations confirms the other half of the
// contract is untouched: the pre-existing accept list still resolves.
func TestTypedDeclName_KeepsRejectingRealDeclarations(t *testing.T) {
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
		// Names that collide with the map, reached through their declaring form.
		`public boolean is(String s) {`:      "is",
		`public void with(String s) {`:       "with",
		`public int lock() {`:                "lock",
		`protected void on(int n) {`:         "on",
		`public static Foo from(String s) {`: "from",
	} {
		if got := typedDeclName(line); got != want {
			t.Errorf("typedDeclName(%q) = %q, want %q", line, got, want)
		}
	}
}
