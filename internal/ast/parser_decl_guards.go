package ast

// Scanning machinery the generic (non-Tree-sitter) parse path dispatches to:
//
// Block 1 — declaration-position guards. typeDeclModifiersOnly is what
// rejects a keyword quoted inside a string literal, and typeDeclName reads
// the declared name once the keyword is known to sit in declaration
// position. PlausibleName keeps punctuation and empty fields from becoming
// a signature.
//
// Block 2 — braceScanner, the string/comment lexer that measures one
// declaration body across successive lines. It exists because scanning each
// line in isolation loses lexer state at the newline.

import (
	"strings"
	"unicode/utf8"
)

// typeDeclModifiers are the tokens that may precede a type-declaration keyword
// on its line: access/abstract/final modifiers plus Rust visibility.
var typeDeclModifiers = map[string]bool{
	"public": true, "private": true, "protected": true, "internal": true,
	"abstract": true, "final": true, "static": true, "sealed": true,
	"partial": true, "readonly": true, "unsafe": true, "const": true,
	"open": true, "base": true, "data": true, "value": true, "augment": true,
	// Kotlin spells a named singleton `companion object Name`, so `companion`
	// sits in the prefix position typeDeclModifiersOnly validates. Without it
	// the guard rejected the line and the whole declaration went unseen.
	"companion": true,
	"pub":       true,
}

// typeDeclModifiersOnly reports whether every field before a type-declaration
// keyword is a modifier, i.e. the keyword sits in DECLARATION position.
//
// This is what rejects a keyword quoted inside a string literal: in
// `System.out.println("the class is not found")` the fields before `class` are
// `System.out.println("the` — an expression token, not a modifier. Without the
// position requirement that line minted `class:<file>::is`, a declaration that
// does not exist. The keyword itself being a well-formed word is exactly why an
// identifier check alone cannot reject it.
func typeDeclModifiersOnly(prefix []string) bool {
	for _, f := range prefix {
		// Rust's parenthesised visibility is one Fields entry: pub(crate),
		// pub(super), pub(self). A multi-field `pub(in a::b)` still fails,
		// which is vanishingly rare and was never claimed as supported.
		if !typeDeclModifiers[f] && !strings.HasPrefix(f, "pub(") {
			return false
		}
	}
	return true
}

// typeDeclPlausibleName rejects a name token that could not have come from a
// declaration head — punctuation, quotes or an empty field — so a stray token
// can never become a signature. Qualified C++ names (`Foo::bar`, `Foo::~Foo`)
// stay accepted, matching plausibleDeclName.
func typeDeclPlausibleName(n string) bool {
	if n == "" || !isIdentStart(n[0]) {
		return false
	}
	for i := 0; i < len(n); i++ {
		if !isIdentChar(n[i]) && n[i] != ':' && n[i] != '~' {
			return false
		}
	}
	return true
}

// typeDeclName returns the declared name of a class/interface/enum head, or ""
// when the line declares none.
//
// It requires the keyword to sit in DECLARATION position (every preceding field
// a modifier) and the taken token to be a plausible identifier. Returning ""
// lets the caller's switch fall through to the LATER arms, which is what keeps
// Kotlin's `fun interface Callback {` working: `fun` is not a modifier here, so
// this arm declines the line and the `lang == LangKotlin` arm below — which
// understands SAM interfaces — handles it. An earlier revision did the position
// check INSIDE this arm's body while gating the case on the old substring test;
// the case then matched, the body produced no name, and the Kotlin arm was never
// reached, silently dropping every `fun interface` (caught by
// TestParseGenericSource_KotlinGenericFunctions).
func typeDeclName(trimmed string) string {
	parts := strings.Fields(trimmed)
	for j, p := range parts {
		// Dart's `mixin` and `extension` and C++'s `namespace` are container
		// declarations too; without them the whole body was invisible to churn,
		// fragility and attribution. The modifier guard still rejects C++'s
		// `using namespace std;`, whose prefix is not a declaration modifier.
		if p != "class" && p != "interface" && p != "enum" && p != "object" && p != "trait" && p != "module" &&
			p != "mixin" && p != "extension" && p != "namespace" {
			continue
		}
		if j+1 >= len(parts) || !typeDeclModifiersOnly(parts[:j]) {
			continue
		}
		k := j + 1
		// Kotlin and Java both spell some types with TWO keywords:
		// `enum class Color`, `enum interface Op`. The keyword found at j is
		// then the outer qualifier, not the declarator, so the name is the field
		// after the SECOND keyword. Reading parts[j+1] instead named the type
		// "class"/"interface" and dropped the real one from the snapshot — a
		// regression introduced when `enum` joined this keyword set (round 6) and
		// untested, because no case covered the two-word spelling.
		if k < len(parts) && (parts[k] == "class" || parts[k] == "interface") {
			k++
		}
		if k >= len(parts) {
			return ""
		}
		n := strings.Split(parts[k], "{")[0]
		n = strings.Split(n, "(")[0]
		n = strings.TrimSpace(n)
		// PHP 8.1 backed enums spell the backing type with no space before the
		// colon (`enum Suit: string`), so parts[k] is literally "Suit:". The
		// colon belongs to the signature, not the identifier: keeping it keyed
		// the node "Suit:" and left the real type absent from the snapshot, so
		// no Diff event was ever attributed to it.
		n = strings.TrimSuffix(n, ":")
		if typeDeclPlausibleName(n) {
			return n
		}
		return ""
	}
	return ""
}

// braceScanner tracks the string/comment lexer state of one declaration body
// across successive lines. Scanning each line in isolation (as the previous
// per-line brace counting did) loses that state at the newline, so a
// '}' inside a multi-line block comment or a multi-line string literal — both
// legal in every language the generic parser handles — was counted as a
// structural closing brace. That truncated the measured body, corrupting
// EndLine/LOC and freezing the body hash, so Diff silently missed later edits
// below the truncation point.
type braceScanner struct {
	inString       byte
	inLineComment  bool
	inBlockComment bool
	// rust makes '\'' a char-literal opener only when it actually opens a
	// char literal ('x', '\n', '\u{1F600}', 'é'). Otherwise it is a lifetime
	// (`fn a<'a>(x: &'a str) {`) whose lone quote used to open a "string"
	// that never closed, swallowing every brace to the end of the file.
	rust bool
}

// rustCharLiteralEnd returns the index of the closing quote when line[i] (a
// '\”) opens a Rust char literal, or -1 when it is a lifetime / label.
func rustCharLiteralEnd(line string, i int) int {
	j := i + 1
	if j >= len(line) {
		return -1
	}
	if line[j] == '\\' {
		// Escapes: '\n', '\'', '\\', '\x7f', '\u{10FFFF}'.
		for k := j + 2; k < len(line) && k <= j+11; k++ {
			if line[k] == '\'' {
				return k
			}
		}
		return -1
	}
	_, size := utf8.DecodeRuneInString(line[j:])
	if k := j + size; k < len(line) && line[k] == '\'' {
		return k
	}
	return -1
}

// lineDelta returns the net '{' minus '}' delta of one line and advances the
// scanner state. Feed lines in body order: inString and inBlockComment carry
// across lines; inLineComment cannot, because the newline separating lines is
// exactly what terminates it.
func (sc *braceScanner) lineDelta(line string) int {
	sc.inLineComment = false
	net := 0
	for i := 0; i < len(line); i++ {
		c := line[i]
		if sc.inLineComment {
			continue
		}
		if sc.inBlockComment {
			if c == '*' && i+1 < len(line) && line[i+1] == '/' {
				sc.inBlockComment = false
				i++
			}
			continue
		}
		if sc.inString != 0 {
			if c == sc.inString {
				backslashes := 0
				for j := i - 1; j >= 0 && line[j] == '\\'; j-- {
					backslashes++
				}
				if backslashes%2 == 0 {
					sc.inString = 0
				}
			}
			continue
		}

		switch c {
		case '/':
			if i+1 < len(line) && line[i+1] == '/' {
				sc.inLineComment = true
				i++
				continue
			}
			if i+1 < len(line) && line[i+1] == '*' {
				sc.inBlockComment = true
				i++
				continue
			}
		case '#':
			// `#` opens a PHP line comment, but a PHP 8 attribute opens with
			// `#[`. Treating the whole line as comment text skipped the
			// declaration's trailing `{`, so braceCount stayed 0, endLine never
			// advanced past the declaration, and the node's hash was frozen over
			// that one line: Diff then saw an unchanged body and emitted no
			// event for any edit inside the method.
			if i+1 < len(line) && line[i+1] == '[' {
				continue // an attribute, not a comment
			}
			sc.inLineComment = true
			continue
		case '\'':
			if sc.rust {
				if end := rustCharLiteralEnd(line, i); end >= 0 {
					i = end // skip the whole literal, including '{' / '}'
				}
				continue // a lifetime or label: not a string opener
			}
			sc.inString = c
			continue
		case '"', '`':
			sc.inString = c
			continue
		case '{':
			net++
		case '}':
			net--
		}
	}
	return net
}
