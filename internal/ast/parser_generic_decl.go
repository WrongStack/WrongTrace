package ast

import "strings"

// Per-language declaration recognition for the generic (non-Tree-sitter) parse
// path. parseGenericSource in parser.go walks a file line by line and asks
// this file two questions per line: does it DECLARE something, and what is
// that thing called. Every per-language rule lives here: the keyword and
// modifier allow-lists, the position and plausibility guards, and the PHP,
// Ruby, Rust and Kotlin declaration recognisers.

// Split out of parser.go, which the guardrail had rated health 0 after 28 write
// cycles: this is where rounds 1 and 6-23 of the bug hunt landed, so keeping the
// rules beside parseGenericSource made it the most-thrashed file in the repo and
// a standing merge-conflict hazard. The split is mechanical - these lines moved
// verbatim - and the accompanying tests in this package pin every rule below.

func phpFunctionDecl(line string) (NodeKind, string) {
	fields := strings.Fields(line)
	for i, field := range fields {
		switch {
		case field == "public", field == "protected", field == "private",
			field == "static", field == "final", field == "abstract":
			continue
		// PHP 8 attributes precede a member declaration. `#[Deprecated]
		// public function old2()` put one in front of `public`, and the loop
		// below returned "" at the first field that was neither a modifier nor
		// `function` -- so the method was declared on no node at all. The same
		// attribute on its own line parsed fine, because the declaration's line
		// then began with `public`: a declaration's visibility depended on an
		// arbitrary line break. Both spellings are idiomatic PHP, so attributes
		// are skipped wherever they appear before the `function` head.
		case strings.HasPrefix(field, "#["):
			continue
		case field == "function":
			if i+1 < len(fields) {
				// PHP marks a by-reference return with `&`, which is part of the
				// signature but not the identifier: `public function &byRef()`
				// declares the method `byRef`. Keeping the sigil keyed the node
				// "&byRef", so the real identifier was absent from the snapshot
				// and no Diff event was ever attributed to that method.
				return NodeFunction, strings.TrimLeft(strings.Split(fields[i+1], "(")[0], "&")
			}
		default:
			return "", ""
		}
		break
	}
	return "", ""
}

// rubyMethodName trims a Ruby name token down to the identifier.
//
// The Ruby arm takes parts[1] and splits on "(", which works while the
// definition continues onto later lines (`def find(name)` -> "find"). A
// ONE-LINE definition leaves the body glued to the name token:
// `def valid?; @ok; end` -> "valid?;", `def tiny; 1; end` -> "tiny;". Those
// became the declared name, so the real identifier never entered the snapshot.
//
// Ruby method names are letters, digits and '_', and may END with '?' or '!'
// (predicate and bang forms) or '=' (setters, `def name=(v)`), so those three
// are kept wherever they appear; everything else is the body and is cut.
func rubyMethodName(tok string) string {
	end := 0
	for i := 0; i < len(tok); i++ {
		c := tok[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '_' || c == '?' || c == '!' || c == '=' {
			end = i + 1
			continue
		}
		break
	}
	return tok[:end]
}

// statementKeywords are words that begin (or sit inside) a statement or
// expression head and therefore can never appear in the modifier/return-type
// prefix of a typed declaration. Any of them before the parenthesised name
// rejects the line: `for (int i = 0; i < size(); i++) {`, `return new Foo(x) {`,
// `using (var s = Open()) {`, `lock (x) {`, `await for (...) {`.
var statementKeywords = map[string]bool{
	"if": true, "for": true, "while": true, "switch": true, "catch": true,
	"else": true, "try": true, "do": true, "return": true, "foreach": true,
	"match": true, "when": true, "using": true, "lock": true, "let": true,
	"new": true, "throw": true, "throws": true, "case": true, "default": true,
	"synchronized": true, "yield": true, "await": true, "in": true, "is": true,
	"as": true, "instanceof": true, "goto": true, "sizeof": true, "typeof": true,
	"delete": true, "assert": true, "fixed": true, "checked": true,
	"unchecked": true, "on": true, "with": true, "var": true, "val": true,
	"elif": true, "unless": true, "until": true, "loop": true, "defer": true,
	"super": true, "this": true, "not": true, "and": true, "or": true,
	"nameof": true, "stackalloc": true, "co_await": true, "co_return": true,
	"finally": true, "from": true, "select": true, "where": true,
}

// typedDeclLanguage reports whether the `Type name(args) {` heuristic applies.
// Only C-family languages declare functions in that shape; Kotlin uses `fun`
// and Rust/Ruby/PHP/Python have their own keywords, where the same shape is
// always a statement head.
func typedDeclLanguage(lang Language) bool {
	switch lang {
	case LangJava, LangCSharp, LangCpp, LangDart:
		return true
	}
	return false
}

// rustQualifiers are the Rust item prefixes that may precede the `fn`/`struct`
// keyword. Visibility is `pub` or `pub(<path>)`; the rest are item modifiers.
// Matched as whole fields so an identifier like `pubkey` is never one.
var rustQualifiers = map[string]bool{
	"pub": true, "async": true, "unsafe": true,
	"const": true, "extern": true, "default": true,
}

// rustDecl classifies a Rust line whose `fn`/`struct` keyword is preceded by
// visibility or modifier tokens — `pub(crate) async fn`, `unsafe fn`,
// `extern "C" fn`, `pub(crate) struct`. The generic arms match only the literal
// heads "pub fn "/"fn " and "pub struct "/"struct ", so every qualified
// declaration was invisible: no node, and therefore no ADDED/MODIFIED/DELETED
// event when the agent edited it. Reachable because `.rs` maps to LangRust and
// Rust has no Tree-sitter grammar, so it always lands in parseGenericSource.
//
// Only the fn/struct keywords are accepted. Rust statement heads share the
// `Type name(args) {` shape (`match x {`, `if let Some(v) = f() {`), which is
// why typedDeclLanguage excludes Rust; a looser test would declare them.
func rustDecl(trimmed string) (NodeKind, string) {
	fields := strings.Fields(trimmed)
	i := 0
	for i < len(fields) {
		f := fields[i]
		if rustQualifiers[f] {
			i++
			continue
		}
		if strings.HasPrefix(f, "pub(") {
			// `pub(crate)`/`pub(super)` are one field; `pub(in path)` spans
			// several, so consume fields until the visibility group closes.
			for i < len(fields) {
				closed := strings.Contains(fields[i], ")")
				i++
				if closed {
					break
				}
			}
			continue
		}
		if len(f) > 1 && f[0] == '"' {
			i++ // extern "C" ABI string
			continue
		}
		break
	}
	// i+1 out of range means no name token follows the keyword.
	if i+1 >= len(fields) {
		return "", ""
	}
	kw := fields[i]
	// `impl<T> Foo<T> {` keeps the type parameter glued to the keyword, so the
	// whole field is "impl<T>" rather than "impl".
	if strings.HasPrefix(kw, "impl<") {
		kw = "impl"
	}
	switch kw {
	case "fn":
		if i == 0 {
			return "", "" // literal "fn " head, which the arms above own
		}
		return NodeFunction, strings.TrimSpace(strings.Split(strings.Split(fields[i+1], "(")[0], "<")[0])
	case "struct":
		if i == 0 {
			return "", "" // literal "struct " head, which the arms above own
		}
		name := strings.Split(fields[i+1], "{")[0]
		return NodeStruct, strings.TrimSpace(strings.Split(name, "<")[0])
	case "trait", "union", "mod", "impl":
		// Rust's remaining item declarations, tracked like `struct`: an edit to a
		// trait or impl body must reach the snapshot and emit a Diff event.
		// Unlike fn/struct these have no literal-head arm above, so i == 0 is
		// the COMMON case ("trait Greeter {") and is accepted here.
		// All four are RESERVED Rust keywords, so no identifier can shadow one
		// and land here — which is what makes widening safe, and why the
		// statement heads this function must not claim (`match x {`,
		// `if let Some(v) = f() {`, `loop {`) still fall through to the default.
		name := fields[i+1]
		if j := strings.IndexAny(name, "{;"); j >= 0 {
			name = name[:j] // `mod util;` and `trait G {` both land here
		}
		if k := strings.IndexByte(name, '<'); k >= 0 {
			name = name[:k]
		}
		if name = strings.TrimSpace(name); name == "" {
			return "", ""
		}
		return NodeClass, name
	}
	return "", ""
}

// kotlinDecl classifies a Kotlin line: a `fun` declaration yields its name as
// a function; `fun interface X` yields a class. Modifiers and annotations may
// precede the keyword (private, suspend, override, @Annot), so the scan is
// token-based. A type-parameter list (`fun <T> name(`, `fun <T, U>name(`,
// `fun <T: Comparable<T>> name(`) is skipped as one balanced group, and an
// extension receiver is kept (`fun <T> List<T>.second(`), matching how
// non-generic extensions (`fun String.shout(`) have always been named.
func kotlinDecl(trimmed string) (NodeKind, string) {
	// Cheap reject before tokenizing: nearly every Kotlin line is not a fun.
	idx := strings.Index(trimmed, "fun")
	if idx < 0 {
		return "", ""
	}
	for idx >= 0 {
		end := idx + 3
		boundaryBefore := idx == 0 || trimmed[idx-1] == ' ' || trimmed[idx-1] == '\t'
		boundaryAfter := end < len(trimmed) && (trimmed[end] == ' ' || trimmed[end] == '\t' || trimmed[end] == '<')
		if boundaryBefore && boundaryAfter {
			return kotlinDeclAfterFun(strings.TrimLeft(trimmed[end:], " \t"))
		}
		next := strings.Index(trimmed[end:], "fun")
		if next < 0 {
			break
		}
		idx = end + next
	}
	return "", ""
}

func kotlinDeclAfterFun(rest string) (NodeKind, string) {
	if rest == "" {
		return "", ""
	}
	if rest[0] == '<' {
		end := skipAngleGroup(rest)
		if end < 0 {
			return "", ""
		}
		rest = strings.TrimLeft(rest[end:], " \t")
	} else if strings.HasPrefix(rest, "interface") && len(rest) > len("interface") &&
		(rest[len("interface")] == ' ' || rest[len("interface")] == '\t') {
		name := kotlinIdent(strings.TrimLeft(rest[len("interface"):], " \t"))
		if name == "" {
			return "", ""
		}
		return NodeClass, name
	}
	name := kotlinFunTarget(rest)
	if name == "" || !isIdentStart(name[0]) {
		return "", ""
	}
	return NodeFunction, name
}

// skipAngleGroup returns the index just past the '>' closing the '<' at s[0],
// or -1 when the group is unbalanced on this line. A '>' that belongs to a
// function-type arrow (`->`) does not close a level.
func skipAngleGroup(s string) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '<':
			depth++
		case '>':
			if i > 0 && s[i-1] == '-' {
				continue
			}
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

// kotlinFunTarget reads `[Receiver.]name` up to the parameter list. Angle
// brackets in a generic receiver (`Map<K, V>.name`) may contain spaces.
func kotlinFunTarget(s string) string {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
		case '(':
			if depth == 0 {
				return s[:i]
			}
		case ' ', '\t', '=', ':', '{':
			if depth == 0 {
				return s[:i]
			}
		}
	}
	if depth != 0 {
		return ""
	}
	return s
}

// kotlinIdent returns the leading identifier of s.
func kotlinIdent(s string) string {
	i := 0
	for i < len(s) && isIdentChar(s[i]) {
		i++
	}
	if i == 0 || !isIdentStart(s[0]) {
		return ""
	}
	return s[:i]
}

// typedDeclName returns the declared name of a `Type name(args) {` line — the
// Dart/Java/C#/C/C++ function shape on the generic path — or "" when the line
// declares nothing. The name is the head of the first field carrying a
// parameter-list opener. Everything before it must read as modifiers and a
// return type: at least one such field, no statement keyword, and no field
// containing anything a type cannot (`=`, operators, a parenthesis, `&&`).
// That is what keeps statement heads and calls nested in them out —
// `for (int i = 0; i < size(); i++) {` would otherwise declare `size` and
// overwrite the real declaration of that name.
func typedDeclName(trimmed string) string {
	// Cheap pre-checks before tokenizing: a declaration head ends in '{' and
	// carries a parameter list; a statement head usually starts with '}' / '('.
	n := len(trimmed)
	if n < 4 || trimmed[n-1] != '{' || !isIdentStart(trimmed[0]) && trimmed[0] != '@' {
		return ""
	}
	if strings.IndexByte(trimmed, '(') < 0 {
		return ""
	}
	fields := strings.Fields(trimmed)
	if statementKeywords[fields[0]] {
		return ""
	}
	for j := 0; j < len(fields); j++ {
		f := fields[j]
		idx := strings.IndexByte(f, '(')
		if idx < 0 {
			if !plausibleTypeToken(f) {
				return ""
			}
			continue
		}
		if j == 0 {
			// `name(args) {` with no return type: a call, a constructor, or a
			// macro — indistinguishable here, so never a declaration.
			return ""
		}
		name := strings.TrimLeft(f[:idx], "*&")
		// The name is NOT filtered against statementKeywords. That map is
		// deliberately overloaded with statement heads from Rust, Python, Swift,
		// Kotlin and JS, and many of its entries are ordinary, legal method names
		// in Java/C#/C++/Dart — the languages typedDeclLanguage gates on. `is`,
		// `on`, `with`, `from`, `select`, `where`, `delete` and `in` are everyday
		// Java method names, and each one made typedDeclName return "", dropping
		// a REAL declaration from the snapshot: editing it then emitted no
		// ADDED/MODIFIED/DELETED Diff event, so it contributed no churn, no
		// thrashing signal and no model attribution.
		//
		// The guard that actually rejects statement heads is the HEAD check above
		// (`statementKeywords[fields[0]]`), plus the `j == 0` "call, not a
		// declaration" rule and plausibleTypeToken. A statement head always
		// STARTS the line, so the head check alone covers every case
		// TestTypedDeclName_RejectsStatementHeads enumerates.
		if !plausibleDeclName(name) {
			return ""
		}
		return name
	}
	return ""
}

// plausibleTypeToken reports whether a whitespace-separated field can be part
// of a modifier/return-type prefix: identifiers, qualified names, generics,
// arrays, pointers/references, nullable markers and annotations.
func plausibleTypeToken(f string) bool {
	if statementKeywords[f] || strings.Contains(f, "&&") || strings.Contains(f, "||") {
		return false
	}
	hasIdent := false
	for i := 0; i < len(f); i++ {
		c := f[i]
		switch {
		case isIdentChar(c):
			hasIdent = true
		case c == '.', c == ':', c == '<', c == '>', c == ',', c == '[', c == ']',
			c == '*', c == '&', c == '?', c == '@', c == '~':
		default:
			return false
		}
	}
	// A bare '*'/'&' separates a C pointer declarator (`char * name(`); any
	// other identifier-free token (`<`, `>`, `::`) is an operator.
	return hasIdent || strings.Trim(f, "*&") == ""
}

// plausibleDeclName accepts an identifier, optionally C++-qualified
// (`Foo::bar`, `Foo::~Foo`). Member access (`a.b`) is a call, not a name.
func plausibleDeclName(name string) bool {
	if name == "" || !isIdentStart(name[0]) {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if isIdentChar(c) || c == ':' || c == '~' {
			continue
		}
		return false
	}
	return true
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentChar(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}
