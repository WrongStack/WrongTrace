package ast

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	sitter "github.com/smacker/go-tree-sitter"

	// Tree-sitter language grammars.
	treeGo "github.com/smacker/go-tree-sitter/golang"
	treeJS "github.com/smacker/go-tree-sitter/javascript"
	treePython "github.com/smacker/go-tree-sitter/python"
	treeTSX "github.com/smacker/go-tree-sitter/typescript/tsx"
)

// NodeKind classifies an AST node for the dashboard and DB.
type NodeKind string

const (
	NodeFunction NodeKind = "function"
	NodeMethod   NodeKind = "method"
	NodeClass    NodeKind = "class"
	NodeStruct   NodeKind = "struct"
	NodeArrowFn  NodeKind = "arrow_function"
)

// Node is a logical, language-agnostic code construct.
type Node struct {
	Signature string   // e.g. "func:auth.ValidateToken(string)"
	Kind      NodeKind // function | method | class | struct | arrow_function
	Body      string   // fallback body for generic parsers and synthetic tests
	StartByte uint32   // byte range in FileSnapshot.RawContent (native parsers)
	EndByte   uint32
	StartLine uint32
	EndLine   uint32
	Hash      string // SHA256 over the normalized body
	LOC       int    // lines of code
}

// FileSnapshot captures the parsed state of a single file at one point in time.
// The Engine holds the previous snapshot per file to compute semantic diffs.
type FileSnapshot struct {
	Path  string
	Nodes map[string]Node // keyed by signature
	Hash  string          // SHA256 of the file's full text
	// RawContent is the source as parsed. It is live only until the snapshot
	// enters the Engine cache, which compresses it into packed. Read the text
	// through Source(), never through this field -- see cache.go.
	RawContent string
	LOC        int      // total lines of code
	packed     []byte   // DEFLATE-compressed RawContent once cached
	rawLen     int      // uncompressed length, for decode buffer sizing
	sortedSigs []string // cached pre-sorted signatures

	// srcMu guards packed/RawContent: Source() reads them on diff goroutines
	// that hold no engine lock, while the LRU sheds them under the engine
	// write lock. Always acquired after the engine lock, never before.
	srcMu sync.RWMutex
}

// Engine owns the Tree-sitter parser pool and the snapshot cache. All
// methods are safe for concurrent use. Parsing is serialized by parseMu: a
// *sitter.Parser wraps a stateful C object that must not be driven from two
// goroutines at once, and the watcher fires debounce callbacks on one
// goroutine per pending path, so concurrent Parse calls are routine.
type Engine struct {
	mu        sync.RWMutex
	snapshots map[string]*FileSnapshot // abs path -> last snapshot
	lru       *sourceLRU               // recency + byte accounting for retained source
	snapLRU   *recencyList             // recency of snapshots, bounds the snapshot count

	parseMu sync.Mutex
	parsers map[Language]*sitter.Parser
	closed  bool
}

var hashBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 4096)
		return &b
	},
}

// NewEngine initializes Tree-sitter parsers for all supported languages.
func NewEngine() (*Engine, error) {
	e := &Engine{
		snapshots: make(map[string]*FileSnapshot),
		lru:       newSourceLRU(),
		snapLRU:   newRecencyList(),
		parsers:   make(map[Language]*sitter.Parser),
	}
	for lang, fn := range map[Language]func() *sitter.Language{
		LangGo:         treeGo.GetLanguage,
		LangTypeScript: treeTSX.GetLanguage,
		LangJavaScript: treeJS.GetLanguage,
		LangPython:     treePython.GetLanguage,
	} {
		if fn == nil {
			continue
		}
		p := sitter.NewParser()
		p.SetLanguage(fn())
		e.parsers[lang] = p
	}
	if len(e.parsers) == 0 {
		return nil, fmt.Errorf("no tree-sitter languages available")
	}
	return e, nil
}

// Close marks the engine closed and drops cached state. It is idempotent;
// after Close, Parse returns an error and snapshot accessors degrade
// gracefully instead of racing or panicking on the nil map.
func (e *Engine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	e.parsers = nil
	e.snapshots = nil
	e.lru = nil
	e.snapLRU = nil
}

// Reset drops all cached file snapshots. Used when switching active projects.
func (e *Engine) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.snapshots != nil {
		e.snapshots = make(map[string]*FileSnapshot)
	}
	if e.lru != nil {
		e.lru.reset()
	}
	if e.snapLRU != nil {
		e.snapLRU.reset()
	}
}

// parserFor returns the parser for a language, or nil when the engine is
// closed or has no grammar for it. TypeScript falls back to the JavaScript
// grammar in this binding.
func (e *Engine) parserFor(lang Language) *sitter.Parser {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed || e.parsers == nil {
		return nil
	}
	p := e.parsers[lang]
	return p
}

// Parse parses a single file's source code and returns a FileSnapshot. Returns
// nil when the file is unsupported (binary, vendor, non-source extension, etc.),
// and an error when the engine is closed. The parse itself is serialized so a
// shared *sitter.Parser is never driven concurrently.
func (e *Engine) Parse(path string, src []byte) (*FileSnapshot, error) {
	return e.ParseHashed(path, src, "")
}

// ParseHashed is Parse for callers that already computed HashBytes(src) --
// typically for the unchanged-content fast path -- so the whole file is not
// SHA-256 hashed a second time. An empty hash is computed here.
func (e *Engine) ParseHashed(path string, src []byte, hash string) (*FileSnapshot, error) {
	e.mu.RLock()
	closed := e.closed
	e.mu.RUnlock()
	if closed {
		return nil, fmt.Errorf("engine closed")
	}

	lang := DetectLanguage(path)
	if lang == LangUnknown {
		return nil, nil
	}
	if hash == "" {
		hash = hashBytes(src)
	}
	parser := e.parserFor(lang)
	if parser == nil {
		// Generic heuristic parser for languages without native tree-sitter C grammar
		return parseGenericSource(path, src, lang, hash), nil
	}

	e.parseMu.Lock()
	tree, err := parser.ParseCtx(context.Background(), nil, src)
	e.parseMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	defer tree.Close()

	root := tree.RootNode()
	raw := string(src)
	snap := &FileSnapshot{
		Path:       path,
		Nodes:      map[string]Node{},
		Hash:       hash,
		RawContent: raw,
		LOC:        strings.Count(raw, "\n") + 1,
	}
	collectNodes(root, src, lang, filepath.Base(path), snap)
	snap.SortedSignatures()
	return snap, nil
}

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
		if p != "class" && p != "interface" && p != "enum" && p != "object" && p != "trait" && p != "module" {
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

// parseGenericSource extracts declarations using robust pattern matching for languages without a compiled Tree-sitter grammar.
func parseGenericSource(path string, src []byte, lang Language, hash ...string) *FileSnapshot {
	h := ""
	if len(hash) > 0 {
		h = hash[0]
	}
	if h == "" {
		h = hashBytes(src)
	}
	raw := string(src)
	lines := strings.Split(raw, "\n")
	base := filepath.Base(path)
	snap := &FileSnapshot{
		Path:       path,
		Nodes:      map[string]Node{},
		Hash:       h,
		RawContent: raw,
		LOC:        len(lines),
	}

	// lineStart[i] is the byte offset of lines[i] in raw. A node spanning lines
	// [a, b) is raw[lineStart[a] : lineStart[b-1]+len(lines[b-1])] -- exactly
	// the newline-joined lines[a:b] -- so nodes carry a byte range into the
	// snapshot's (compressed, budgeted) source instead of their own uncompressed
	// copy. Nested declarations used to duplicate their enclosing text again.
	lineStart := make([]int, len(lines))
	for i, off := 0, 0; i < len(lines); i++ {
		lineStart[i] = off
		off += len(lines[i]) + 1
	}

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") {
			continue
		}
		// `#` opens a PHP line comment, so lines beginning with it were skipped as
		// comments. But PHP 8 attributes open with `#[`, so EVERY inline-annotated
		// declaration -- `#[Deprecated] public function old2() {` -- was discarded
		// here before reaching the dispatch switch at all. Only a `#` NOT followed
		// by `[` is a comment; the two spellings are distinguishable, and PHP allows
		// a comment body to start with anything, so the test must be exactly this.
		if strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(trimmed, "#[") {
			continue
		}

		var kind NodeKind
		var name string

		switch {
		case strings.HasPrefix(trimmed, "pub fn ") || strings.HasPrefix(trimmed, "fn "):
			kind = NodeFunction
			parts := strings.Fields(trimmed)
			for j, p := range parts {
				if p == "fn" && j+1 < len(parts) {
					name = strings.Split(strings.Split(parts[j+1], "(")[0], "<")[0]
					break
				}
			}
		case strings.HasPrefix(trimmed, "pub struct ") || strings.HasPrefix(trimmed, "struct "):
			kind = NodeStruct
			parts := strings.Fields(trimmed)
			for j, p := range parts {
				if p == "struct" && j+1 < len(parts) {
					name = strings.Split(parts[j+1], "{")[0]
					name = strings.Split(name, "<")[0]
					break
				}
			}
		case typeDeclName(trimmed) != "":
			// `interface` and `enum` declare types exactly as `class` does. Java,
			// C# and Dart have no Tree-sitter grammar, so every
			// `public interface Repo {` / `public enum Color {` reached this arm's
			// predecessor list empty and produced NO node: the declaration was
			// absent from the snapshot, so an edit anywhere inside it matched no
			// signature and emitted no ADDED/MODIFIED/DELETED event -- no churn
			// analytics, no thrashing/fragility signal, no model attribution. The
			// same class of blindness round 89 fixed for Kotlin `fun`.
			//
			// The gate above is a bare SUBSTRING test, so it also fires on a line
			// that merely MENTIONS the word inside a string literal:
			//
			//	System.out.println("the class is not found");  -> class:Repo.java::is
			//	warn("unknown enum value");                  -> class:Repo.java::value");
			//
			// Both phantoms entered the snapshot and emitted ADDED/DELETED Diff
			// events for symbols that do not exist, corrupting churn analytics,
			// fragility scoring and model attribution. The repo fixed this same
			// failure mode for statement heads (parser_generic_regression_test.go)
			// but never for string literals, and rounds 1/6 widened this arm
			// without the guard.
			//
			// Both tokens in those examples are perfectly well-formed words, so an
			// identifier check alone cannot reject them: the keyword must also sit
			// in DECLARATION position, i.e. every field before it is a modifier.
			kind = NodeClass
			name = typeDeclName(trimmed)
		case lang == LangPHP:
			kind, name = phpFunctionDecl(trimmed)
		case strings.HasPrefix(trimmed, "function ") || strings.HasPrefix(trimmed, "public function ") || strings.HasPrefix(trimmed, "private function "):
			kind = NodeFunction
			parts := strings.Fields(trimmed)
			for j, p := range parts {
				if p == "function" && j+1 < len(parts) {
					name = strings.Split(parts[j+1], "(")[0]
					break
				}
			}
		case strings.HasPrefix(trimmed, "def ") || strings.HasPrefix(trimmed, "def self."):
			kind = NodeFunction
			parts := strings.Fields(trimmed)
			if len(parts) > 1 {
				name = strings.Split(parts[1], "(")[0]
				// `def self.make` declares the method `make`: `self.` is the
				// receiver, part of the signature and not part of the
				// identifier. Keeping it keyed the node "self.make", so the real
				// method name was absent from the snapshot — the same split
				// identity round 13 fixed for PHP's `&` and `:` sigils.
				name = strings.TrimPrefix(name, "self.")
				name = rubyMethodName(name)
			}
		case lang == LangRust:
			// Rust items carry visibility/modifier tokens before the keyword
			// (`pub(crate) async fn f(`, `unsafe fn g(`, `extern "C" fn h(`,
			// `pub(super) struct S`). The literal-head arms above match only
			// "pub fn "/"fn " and "pub struct "/"struct ", so every qualified
			// declaration produced NO node and its edits emitted no Diff
			// event — the same defect round 89 fixed for Kotlin `fun`.
			kind, name = rustDecl(trimmed)
		case lang == LangKotlin:
			// Kotlin fun declarations: the only node kind for most .kt files,
			// whose top-level funs previously produced no nodes at all — the
			// files mapped onto this parser in the round-89 extension were
			// "reaching the pipeline" as classes only, so function edits
			// emitted no Diff events. `fun interface X` is a SAM interface
			// declaration, not a function named "interface".
			kind, name = kotlinDecl(trimmed)
		case typedDeclLanguage(lang):
			// Typed `Type name(args) {` declarations (Dart, Java, C#, C/C++):
			// `void main() {` and `int inc(int v) {` were as invisible as the
			// Kotlin funs above. Gated by language: Rust/Ruby/PHP/Python heads
			// (`match parse(x) {`, `if let Some(v) = f() {`) share the shape
			// but never declare anything.
			if n := typedDeclName(trimmed); n != "" {
				kind, name = NodeFunction, n
			}
		}

		if name != "" && kind != "" {
			name = strings.TrimSpace(name)
			sig := fmt.Sprintf("%s:%s::%s", kind, base, name)

			startLine := i + 1
			endLine := i + 1

			if strings.Contains(line, "{") {
				// The scanner carries string/comment state across lines, so a
				// '}' inside a multi-line block comment or multi-line string
				// is no longer mistaken for the end of the body.
				scanner := braceScanner{rust: lang == LangRust}
				braceCount := scanner.lineDelta(line)
				j := i + 1
				for ; j < len(lines) && braceCount > 0; j++ {
					braceCount += scanner.lineDelta(lines[j])
				}
				endLine = j
			} else if lang == LangRuby {
				// Ruby closes blocks with `end`, never `}`, so the brace scan above
				// never fired and EVERY Ruby node kept endLine on its declaration
				// line. The span then froze the body hash over that one line, and
				// since Diff decides MODIFIED by comparing BodyHash, no edit inside
				// any Ruby method or class body could ever produce an event —
				// Ruby's churn/thrash/attribution signals were structurally dark,
				// and failed silently because the declaration WAS in the snapshot.
				//
				// Match the `end` at the DECLARATION's own indentation: that is the
				// one that closes this block, so a nested `end` (an `if` inside a
				// `def`) is skipped rather than closing the outer node early.
				indent := len(line) - len(strings.TrimLeft(line, " \t"))
				for k := i + 1; k < len(lines); k++ {
					trimmed := strings.TrimSpace(lines[k])
					if trimmed == "" || strings.HasPrefix(trimmed, "#") {
						continue
					}
					if len(lines[k])-len(strings.TrimLeft(lines[k], " \t")) != indent {
						continue
					}
					if trimmed == "end" || strings.HasSuffix(trimmed, " end") {
						endLine = k + 1
						break
					}
				}
			}

			startByte := lineStart[startLine-1]
			endByte := lineStart[endLine-1] + len(lines[endLine-1])
			nodeHash := hashNormalizedBodyBytes(src[startByte:endByte], lang)

			snap.Nodes[sig] = Node{
				Signature: sig,
				Kind:      kind,
				StartByte: uint32(startByte),
				EndByte:   uint32(endByte),
				StartLine: uint32(startLine),
				EndLine:   uint32(endLine),
				Hash:      nodeHash,
				LOC:       endLine - startLine + 1,
			}
		}
	}

	snap.SortedSignatures()
	return snap
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

// Snapshot returns the cached snapshot for a file, if any.
func (e *Engine) Snapshot(path string) (*FileSnapshot, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	s, ok := e.snapshots[path]
	return s, ok
}

// AllSnapshots returns a shallow copy of all cached snapshots.
func (e *Engine) AllSnapshots() map[string]*FileSnapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.snapshots == nil {
		return nil
	}
	out := make(map[string]*FileSnapshot, len(e.snapshots))
	for k, v := range e.snapshots {
		out[k] = v
	}
	return out
}

// ForEachSnapshot invokes fn for each cached snapshot under a read lock without allocating a map copy.
func (e *Engine) ForEachSnapshot(fn func(path string, snap *FileSnapshot) bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.snapshots == nil {
		return
	}
	for k, v := range e.snapshots {
		if !fn(k, v) {
			break
		}
	}
}

// SnapshotEntry pairs a cached snapshot with its cache key.
type SnapshotEntry struct {
	Path string
	Snap *FileSnapshot
}

// SnapshotList returns the cached snapshots as a slice, holding the read lock
// only for the pointer copy. Snapshots are immutable once cached, so callers
// doing heavy per-file work should use this instead of ForEachSnapshot: a long
// walk under the read lock stalls SetSnapshot, and a waiting writer in turn
// blocks every new reader.
func (e *Engine) SnapshotList() []SnapshotEntry {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]SnapshotEntry, 0, len(e.snapshots))
	for k, v := range e.snapshots {
		out = append(out, SnapshotEntry{Path: k, Snap: v})
	}
	return out
}

// SnapshotCount returns the number of files currently cached in memory.
func (e *Engine) SnapshotCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.snapshots)
}

// SetSnapshot stores a freshly-parsed snapshot for diffing on the next
// event. A no-op after Close: the map is dropped then, and assigning into a
// nil map would panic.
func (e *Engine) SetSnapshot(s *FileSnapshot) {
	if s == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.snapshots == nil {
		return
	}
	// Compress before retention and charge the delta against the byte budget.
	// Replacing an existing snapshot must refund the outgoing one first, or a
	// file edited in a loop would inflate the accounting without bound.
	if prev, ok := e.snapshots[s.Path]; ok && e.lru != nil {
		e.lru.remove(s.Path, prev.retainedBytes())
	}
	s.pack()
	e.snapshots[s.Path] = s
	if e.lru != nil {
		e.lru.touch(s.Path, s.retainedBytes())
		e.lru.evictTo(sourceBudgetBytes, e.snapshots)
	}
	if e.snapLRU != nil {
		e.snapLRU.touch(s.Path)
		e.snapLRU.evictSnapshots(snapshotLimit, e.snapshots, e.lru)
	}
}

// Forget removes the cached snapshot for a deleted file.
func (e *Engine) Forget(path string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.lru != nil {
		if snap, ok := e.snapshots[path]; ok {
			e.lru.remove(path, snap.retainedBytes())
		}
	}
	if e.snapLRU != nil {
		e.snapLRU.remove(path)
	}
	delete(e.snapshots, path)
}

// collectNodes walks the tree recursively and yields one Node per relevant
// declaration. The signature format is "kind:file::name(arg-types)" where
// arg types are normalized so trivial rename-only changes still register as
// the same signature.
func collectNodes(root *sitter.Node, src []byte, lang Language, file string, out *FileSnapshot) {
	cursor := sitter.NewTreeCursor(root)
	defer cursor.Close()

	walk(cursor, src, lang, file, out, 0)
}

// walk is an explicit-stack traversal that keeps allocations minimal with a depth bound.
func walk(cursor *sitter.TreeCursor, src []byte, lang Language, file string, out *FileSnapshot, depth int) {
	if depth > 250 {
		return
	}
	node := cursor.CurrentNode()
	kindStr := node.Type()
	kind, ok := classifyNode(lang, kindStr, node)
	if ok {
		sig := buildSignature(lang, file, kind, node, src)
		a, b := node.StartByte(), node.EndByte()
		var nodeHash string
		if a <= b && b <= uint32(len(src)) {
			nodeHash = hashNormalizedBodyBytes(src[a:b], lang)
		} else {
			nodeHash = hashNormalizedBody(sliceText(node, src), lang)
		}
		out.Nodes[sig] = Node{
			Signature: sig,
			Kind:      kind,
			StartByte: a,
			EndByte:   b,
			StartLine: node.StartPoint().Row + 1,
			EndLine:   node.EndPoint().Row + 1,
			Hash:      nodeHash,
			LOC:       int(node.EndPoint().Row-node.StartPoint().Row) + 1,
		}
	}

	// Recurse into children.
	if cursor.GoToFirstChild() {
		for {
			walk(cursor, src, lang, file, out, depth+1)
			if !cursor.GoToNextSibling() {
				cursor.GoToParent()
				return
			}
		}
	}
}

// classifyNode decides whether a given Tree-sitter node represents a function,
// method, class, or struct and returns its NodeKind. The boolean is false when
// the node is a non-declaration intermediate (statements, blocks, etc.).
func classifyNode(lang Language, typeStr string, n *sitter.Node) (NodeKind, bool) {
	switch lang {
	case LangGo:
		switch typeStr {
		case "function_declaration":
			return NodeFunction, true
		case "method_declaration":
			return NodeMethod, true
		case "type_spec":
			// type Foo struct {...} → struct
			if isStructType(n, "") {
				return NodeStruct, true
			}
		}
	case LangTypeScript, LangJavaScript:
		switch typeStr {
		case "function_declaration":
			return NodeFunction, true
		case "method_definition":
			return NodeMethod, true
		case "arrow_function":
			// Only count named arrow functions assigned to a const/let.
			if hasNamedAssignment(n) {
				return NodeArrowFn, true
			}
		case "class_declaration":
			return NodeClass, true
		case "interface_declaration":
			// `interface Repo { … }` is a first-class TypeScript declaration and
			// the most-used type form in the language, but it had no case here,
			// so it was classified as a non-declaration intermediate: the node
			// never entered the snapshot, and an edit inside an interface
			// matched no signature and emitted NO ADDED/MODIFIED/DELETED event.
			// No churn, no fragility/thrashing signal, no model attribution —
			// the same blindness this parser fixed repeatedly on the generic path
			// for class/interface/enum/object/trait/module.
			//
			// A `type X = Y` ALIAS is deliberately not included: it is a
			// different tree-sitter node type and carries no body to diff.
			return NodeClass, true
		}
	case LangPython:
		switch typeStr {
		case "function_definition":
			return NodeFunction, true
		case "class_definition":
			return NodeClass, true
		}
	}
	return "", false
}

// buildSignature produces a stable identifier like:
//
//	func:auth.go::ValidateToken(string)
//	method:server.go::(*Server).Handle(string)
//	class:ui.tsx::Dashboard
//
// Renames of the parent package or class shift the signature, which is
// intentional: surviving a rename is not the same as surviving intact.
func buildSignature(lang Language, file string, kind NodeKind, n *sitter.Node, src []byte) string {
	name := extractName(lang, n, src)
	switch lang {
	case LangGo:
		// For Go methods, prepend the receiver type to disambiguate.
		if kind == NodeMethod {
			if recv := receiverType(n, src); recv != "" {
				return fmt.Sprintf("%s:%s::%s.%s", kind, file, recv, name)
			}
		}
		return fmt.Sprintf("%s:%s::%s", kind, file, name)
	case LangPython, LangTypeScript, LangJavaScript:
		if kind != NodeClass {
			var owners []string
			for parent := n.Parent(); parent != nil; parent = parent.Parent() {
				switch parent.Type() {
				case "function_definition", "class_definition", "function_declaration", "class_declaration", "method_definition":
				case "arrow_function":
					if !hasNamedAssignment(parent) {
						continue
					}
				default:
					continue
				}
				owners = append(owners, extractName(lang, parent, src))
			}
			for i := 0; i < len(owners)/2; i++ {
				owners[i], owners[len(owners)-1-i] = owners[len(owners)-1-i], owners[i]
			}
			if len(owners) > 0 {
				name = strings.Join(owners, ".") + "." + name
			}
		}
		return fmt.Sprintf("%s:%s::%s", kind, file, name)
	default:
		if scope := enclosingTypeName(n, src); scope != "" && kind != NodeClass {
			name = scope + "." + name
		}
		return fmt.Sprintf("%s:%s::%s", kind, file, name)
	}
}

// enclosingTypeName disambiguates same-named methods declared in different
// classes. Walking parents is allocation-free and avoids the much more costly
// global symbol-table pass for this common collision case.
func enclosingTypeName(n *sitter.Node, src []byte) string {
	for parent := n.Parent(); parent != nil; parent = parent.Parent() {
		switch parent.Type() {
		case "class_declaration", "class_definition":
			if name := parent.ChildByFieldName("name"); name != nil {
				return string(src[name.StartByte():name.EndByte()])
			}
		}
	}
	return ""
}

// extractName returns the best-available identifier for a declaration: the
// function/method/class name, or the variable name for an arrow function.
func extractName(lang Language, n *sitter.Node, src []byte) string {
	switch lang {
	case LangGo:
		if c := n.ChildByFieldName("name"); c != nil {
			return string(src[c.StartByte():c.EndByte()])
		}
	case LangTypeScript, LangJavaScript:
		if c := n.ChildByFieldName("name"); c != nil {
			return string(src[c.StartByte():c.EndByte()])
		}
		if kindStr := n.Type(); kindStr == "arrow_function" {
			if p := n.Parent(); p != nil {
				if c := p.ChildByFieldName("name"); c != nil {
					return string(src[c.StartByte():c.EndByte()])
				}
				// variable_declarator: name is the first child.
				for i := 0; i < int(p.ChildCount()); i++ {
					ch := p.Child(i)
					if ch.Type() == "identifier" {
						return string(src[ch.StartByte():ch.EndByte()])
					}
				}
			}
		}
	case LangPython:
		if c := n.ChildByFieldName("name"); c != nil {
			return string(src[c.StartByte():c.EndByte()])
		}
	}
	// Fallback to a stable synthetic identifier from the byte range.
	return fmt.Sprintf("anon@%d-%d", n.StartByte(), n.EndByte())
}

// receiverType pulls the type token from a Go method receiver list. We keep the
// pointer marker ("*Foo") so (*Server).Handle differs from Server.Handle.
func receiverType(n *sitter.Node, src []byte) string {
	// Go tree-sitter emits the receiver as a "parameter_list" under
	// "method_declaration". Walk its children to find the type.
	for i := 0; i < int(n.ChildCount()); i++ {
		ch := n.Child(i)
		if ch.Type() == "parameter_list" {
			for j := 0; j < int(ch.ChildCount()); j++ {
				sch := ch.Child(j)
				if sch.Type() == "parameter_declaration" {
					// The type is the last child in the parameter_declaration.
					idx := int(sch.ChildCount()) - 1
					for ; idx >= 0; idx-- {
						t := sch.Child(idx)
						if t.Type() != "comment" {
							return string(src[t.StartByte():t.EndByte()])
						}
					}
				}
			}
		}
	}
	return ""
}

// hasNamedAssignment reports whether an arrow function sits inside a binding
// pattern that gives it a real identifier. Anonymous inline arrow functions
// (callbacks, IIFEs) are intentionally excluded to keep signal high.
func hasNamedAssignment(n *sitter.Node) bool {
	p := n.Parent()
	if p == nil {
		return false
	}
	switch p.Type() {
	case "variable_declarator", "assignment_expression":
		return true
	}
	// JSX prop callbacks (onClick={...}) are useful to track.
	if p.Type() == "jsx_attribute" || p.Type() == "pair" {
		return true
	}
	return false
}

// isStructType checks whether a Go type_spec introduces a struct. Tree-sitter
// exposes the type body via the "type" field; we confirm the keyword.
func isStructType(n *sitter.Node, _ string) bool {
	for i := 0; i < int(n.ChildCount()); i++ {
		ch := n.Child(i)
		if ch.Type() == "struct_type" {
			return true
		}
	}
	return false
}

// sliceText returns the source text covered by a node.
func sliceText(n *sitter.Node, src []byte) string {
	a, b := n.StartByte(), n.EndByte()
	if a > b || b > uint32(len(src)) {
		return ""
	}
	return string(src[a:b])
}

// hashNormalizedBody strips comments and collapses whitespace directly in a pooled byte buffer
// and returns the hex-encoded SHA256 without intermediate string allocations.
func hashNormalizedBody(s string, lang Language) string {
	return hashNormalizedBodyBytes([]byte(s), lang)
}

// hashNormalizedBodyBytes strips comments and collapses whitespace directly from a byte slice into a pooled buffer
// and returns the hex-encoded SHA256 without string allocations.
func hashNormalizedBodyBytes(b []byte, lang Language) string {
	bufPtr := hashBufPool.Get().(*[]byte)
	buf := (*bufPtr)[:0]
	if cap(buf) < len(b) {
		buf = make([]byte, 0, len(b))
	}
	defer func() {
		*bufPtr = buf
		hashBufPool.Put(bufPtr)
	}()

	prevSpace := false
	inLineComment := false
	inBlockComment := false
	inString := byte(0)

	for i := 0; i < len(b); i++ {
		c := b[i]
		if inLineComment {
			if c == '\n' {
				inLineComment = false
			}
			continue
		}
		if inBlockComment {
			if c == '*' && i+1 < len(b) && b[i+1] == '/' {
				inBlockComment = false
				i++
			}
			continue
		}
		if inString != 0 {
			buf = append(buf, c)
			if c == inString {
				backslashes := 0
				for j := i - 1; j >= 0 && b[j] == '\\'; j-- {
					backslashes++
				}
				if backslashes%2 == 0 {
					inString = 0
				}
			}
			continue
		}
		switch c {
		case '/':
			if lang != LangPython && i+1 < len(b) && b[i+1] == '/' {
				inLineComment = true
				i++
				continue
			}
			if lang != LangPython && i+1 < len(b) && b[i+1] == '*' {
				inBlockComment = true
				i++
				continue
			}
		case '#':
			// Python-only line comment. In JS/TS '#' starts a private field
			// (this.#x) — stripping it there corrupts the hash.
			if lang == LangPython {
				inLineComment = true
				continue
			}
		case '"', '\'', '`':
			inString = c
			buf = append(buf, c)
			continue
		}
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			if !prevSpace {
				buf = append(buf, ' ')
				prevSpace = true
			}
			continue
		}
		buf = append(buf, c)
		prevSpace = false
	}

	trimmed := bytes.TrimSpace(buf)
	sum := sha256.Sum256(trimmed)
	return hex.EncodeToString(sum[:])
}

// normalizeForHash strips comments and collapses whitespace so cosmetic-only
// edits (formatting, comment tweaks) do not register as semantic changes.
func normalizeForHash(s string, lang Language) string {
	bufPtr := hashBufPool.Get().(*[]byte)
	buf := (*bufPtr)[:0]
	if cap(buf) < len(s) {
		buf = make([]byte, 0, len(s))
	}
	defer func() {
		*bufPtr = buf
		hashBufPool.Put(bufPtr)
	}()

	prevSpace := false
	inLineComment := false
	inBlockComment := false
	inString := byte(0)

	for i := 0; i < len(s); i++ {
		c := s[i]
		if inLineComment {
			if c == '\n' {
				inLineComment = false
			}
			continue
		}
		if inBlockComment {
			if c == '*' && i+1 < len(s) && s[i+1] == '/' {
				inBlockComment = false
				i++
			}
			continue
		}
		if inString != 0 {
			buf = append(buf, c)
			if c == inString {
				backslashes := 0
				for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
					backslashes++
				}
				if backslashes%2 == 0 {
					inString = 0
				}
			}
			continue
		}
		switch c {
		case '/':
			if lang != LangPython && i+1 < len(s) && s[i+1] == '/' {
				inLineComment = true
				i++
				continue
			}
			if lang != LangPython && i+1 < len(s) && s[i+1] == '*' {
				inBlockComment = true
				i++
				continue
			}
		case '#':
			if lang == LangPython {
				inLineComment = true
				continue
			}
		case '"', '\'', '`':
			inString = c
			buf = append(buf, c)
			continue
		}
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			if !prevSpace {
				buf = append(buf, ' ')
				prevSpace = true
			}
			continue
		}
		buf = append(buf, c)
		prevSpace = false
	}

	return string(bytes.TrimSpace(buf))
}

// HashBytes computes the SHA256 hex digest for arbitrary byte content.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hashBytes(b []byte) string {
	return HashBytes(b)
}

// SortedSignatures returns the signatures of a snapshot in lexical order.
// Useful for deterministic iteration during diffing and tests.
func (f *FileSnapshot) SortedSignatures() []string {
	if f == nil {
		return nil
	}
	if f.sortedSigs != nil && len(f.sortedSigs) == len(f.Nodes) {
		return f.sortedSigs
	}
	out := make([]string, 0, len(f.Nodes))
	for k := range f.Nodes {
		out = append(out, k)
	}
	sort.Strings(out)
	f.sortedSigs = out
	return out
}
