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

		// A PHP declaration can share the `<?php` opener's line (`<?php class C {`,
		// `<?php function f() {`). `<?php` is not a declaration modifier, so the
		// head guards rejected the whole line and the declaration produced NO node:
		// no body hash, no churn event, no model attribution. Drop the opener tag
		// (and a trailing `?>` closer) so the head is what the guards expect.
		// Gated on PHP: no other supported language opens a line with that tag.
		if lang == LangPHP {
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "<?php"))
			trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, "?>"))
		}

		// A declaration head does not always carry its body-opening '{'. That is
		// a brace STYLE, not a language feature: Microsoft's own C# convention,
		// PHP PSR-12 class bodies and much Java and Dart put the brace on the
		// NEXT line. braceLine is that line (the first following non-blank line,
		// when it opens with '{'), or -1. Ruby is excluded: `end` closes its
		// blocks, so a later '{' there is a hash literal, not the body — its
		// span has its own `end` scan below.
		braceLine := -1
		if !strings.Contains(line, "{") && lang != LangRuby {
			for k := i + 1; k < len(lines); k++ {
				if t := strings.TrimSpace(lines[k]); t != "" {
					if strings.HasPrefix(t, "{") {
						braceLine = k
					}
					break
				}
			}
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
		case rubyDefDecl(trimmed) != "":
			kind = NodeFunction
			parts := strings.Fields(rubyDefDecl(trimmed))
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
			if n := typedDeclNameWithBrace(trimmed, braceLine >= 0); n != "" {
				kind, name = NodeFunction, n
			}
		}

		if name != "" && kind != "" {
			name = strings.TrimSpace(name)
			sig := fmt.Sprintf("%s:%s::%s", kind, base, name)

			startLine := i + 1
			endLine := i + 1

			// Start the body scan at the line that actually carries '{'. When the
			// head has none, that is braceLine, the following line that opens the
			// body (Allman / PSR-12 / Microsoft C# style). Treating such a head
			// as brace-less left the span on the head line, so the node's body
			// hash was frozen and Diff — which decides MODIFIED by comparing that
			// hash — could never emit an event for an edit inside the body,
			// silently: the declaration WAS in the snapshot. bodyOpen is -1 when
			// no brace opens the body, which is Ruby's case (`end` closes it) and
			// the case of a head with no block body at all.
			bodyOpen := i
			if !strings.Contains(line, "{") {
				bodyOpen = braceLine
			}
			if bodyOpen >= 0 {
				// The scanner carries string/comment state across lines, so a
				// '}' inside a multi-line block comment or multi-line string
				// is no longer mistaken for the end of the body.
				scanner := braceScanner{rust: lang == LangRust}
				braceCount := scanner.lineDelta(lines[bodyOpen])
				j := bodyOpen + 1
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

// rubyDefDecl returns the `def` head of a Ruby declaration line with any
// visibility modifiers stripped, or "" when the line declares nothing.
//
// Ruby's `private def foo`, `protected def foo` and `module_function def foo`
// spellings are ordinary style, and matching only a bare `def ` prefix made
// every one of them invisible: the method produced no node, so its edits
// emitted no Diff event, its body hash never moved and no model could be
// attributed to it.
func rubyDefDecl(trimmed string) string {
	head := trimmed
	for _, mod := range []string{"private_class_method ", "module_function ", "private ", "protected ", "public "} {
		if strings.HasPrefix(head, mod) {
			head = strings.TrimSpace(head[len(mod):])
			break
		}
	}
	if strings.HasPrefix(head, "def ") || strings.HasPrefix(head, "def self.") {
		return head
	}
	return ""
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
	spans := collectLiteralSpans(root, lang)
	cursor := sitter.NewTreeCursor(root)
	defer cursor.Close()

	walk(cursor, src, lang, file, out, spans, 0)
}

// litSpan is a byte range the grammar identified as a comment or as a
// string-like literal (string, template, regex, rune).
type litSpan struct {
	start, end uint32
	comment    bool
}

// collectLiteralSpans lists, in source order, every comment and string-like
// literal in the tree. Native node hashes are normalized from these instead
// of re-lexing the bytes: a lexer that does not know regex literals or
// template nesting read `/^https?:\/\//` as a `//` comment and `/["']/` as an
// open string, silently dropping the real code after it from the hash.
// Literals are not descended, so a comment inside a template substitution
// stays part of the verbatim literal.
func collectLiteralSpans(root *sitter.Node, lang Language) []litSpan {
	var spans []litSpan
	cur := sitter.NewTreeCursor(root)
	defer cur.Close()
	for {
		n := cur.CurrentNode()
		if comment, ok := literalNodeKind(lang, n); ok {
			spans = append(spans, litSpan{start: n.StartByte(), end: n.EndByte(), comment: comment})
		} else if cur.GoToFirstChild() {
			continue
		}
		for !cur.GoToNextSibling() {
			if !cur.GoToParent() {
				return spans
			}
		}
	}
}

// literalNodeKind classifies n as a comment (true) or a verbatim literal
// (false); ok is false for every other node.
func literalNodeKind(lang Language, n *sitter.Node) (comment, ok bool) {
	if !n.IsNamed() {
		return false, false
	}
	switch t := n.Type(); t {
	case "comment":
		return true, true
	case "html_comment":
		return true, lang == LangJavaScript || lang == LangTypeScript
	case "interpreted_string_literal", "raw_string_literal", "rune_literal":
		return false, lang == LangGo
	case "template_string", "regex":
		return false, lang == LangJavaScript || lang == LangTypeScript
	case "string":
		return false, lang != LangGo
	}
	return false, false
}

// walk is an explicit-stack traversal that keeps allocations minimal with a depth bound.
func walk(cursor *sitter.TreeCursor, src []byte, lang Language, file string, out *FileSnapshot, spans []litSpan, depth int) {
	if depth > 250 {
		return
	}
	node := cursor.CurrentNode()
	kindStr := node.Type()
	kind, ok := classifyNode(lang, kindStr, node)
	if ok {
		sig := buildSignature(lang, file, kind, node, src)
		// Declarations sharing a name in one scope (getter/setter pairs,
		// Python property setters, several Go init funcs) used to overwrite
		// each other, so an edit to the overwritten one emitted nothing. Later
		// ones get an occurrence suffix in document order.
		if _, taken := out.Nodes[sig]; taken {
			base := sig
			for n := 2; ; n++ {
				if sig = fmt.Sprintf("%s#%d", base, n); out.Nodes[sig].Signature == "" {
					break
				}
			}
		}
		span := node
		if p := node.Parent(); lang == LangPython && p != nil && p.Type() == "decorated_definition" {
			// Decorators belong to the parent node; they are part of what the
			// declaration says (route, fixture scope, cache size), so an edit
			// to them is an edit to this declaration.
			span = p
		}
		a, b := span.StartByte(), span.EndByte()
		var nodeHash string
		if a <= b && b <= uint32(len(src)) {
			nodeHash = hashNormalized(src[a:b], lang, spansWithin(spans, a, b), a, false)
		} else {
			nodeHash = hashNormalizedBody(sliceText(span, src), lang)
		}
		out.Nodes[sig] = Node{
			Signature: sig,
			Kind:      kind,
			StartByte: a,
			EndByte:   b,
			StartLine: span.StartPoint().Row + 1,
			EndLine:   span.EndPoint().Row + 1,
			Hash:      nodeHash,
			LOC:       int(span.EndPoint().Row-span.StartPoint().Row) + 1,
		}
	}

	// Recurse into children.
	if cursor.GoToFirstChild() {
		for {
			walk(cursor, src, lang, file, out, spans, depth+1)
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
			// type Store interface {...}: a method-set change is as much an
			// API change as a struct field change, but it never became a node.
			if hasChildType(n, "interface_type") {
				return NodeClass, true
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
			if isNamedExpression(n) {
				return NodeArrowFn, true
			}
		case "function_expression", "function":
			// `const h = function () {}` and `export default function () {}`
			// are function EXPRESSIONS; at top level nothing encloses them, so
			// without a case an edit to one emitted nothing. IsNamed skips the
			// anonymous `function` keyword token.
			if n.IsNamed() && isNamedExpression(n) {
				return NodeFunction, true
			}
		case "class":
			// `export default class {}` / `const K = class {}`: same gap.
			if n.IsNamed() && isNamedExpression(n) {
				return NodeClass, true
			}
		case "class_declaration", "abstract_class_declaration":
			// `abstract class` is its own tree-sitter node type; without it the
			// class never entered the snapshot and its methods lost their owner,
			// so same-named methods of two abstract classes collided.
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
		// Classes carry the owner chain too: nested classes (Django `class
		// Meta`, classes built in factories) were keyed by bare name, so
		// inserting one above re-keyed every later one as MODIFIED.
		var owners []string
		for parent := n.Parent(); parent != nil; parent = parent.Parent() {
			switch parent.Type() {
			case "function_definition", "class_definition", "function_declaration", "class_declaration", "abstract_class_declaration", "method_definition":
			case "arrow_function", "function_expression", "function", "class":
				if !parent.IsNamed() || !isNamedExpression(parent) {
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
		if kindStr := n.Type(); kindStr == "arrow_function" || kindStr == "function_expression" || kindStr == "function" || kindStr == "class" {
			if p := n.Parent(); p != nil {
				if c := p.ChildByFieldName("name"); c != nil {
					return string(src[c.StartByte():c.EndByte()])
				}
				if p.Type() == "export_statement" {
					return "default" // export default function/class/arrow
				}
				// `obj.h = () =>` / `this.x = () =>` and `{ get: () => }` name
				// the function by their target; falling through to the
				// byte-range fallback re-keyed it on every edit above it.
				switch p.Type() {
				case "assignment_expression":
					if c := p.ChildByFieldName("left"); c != nil {
						return string(src[c.StartByte():c.EndByte()])
					}
				case "pair":
					if c := p.ChildByFieldName("key"); c != nil {
						return string(src[c.StartByte():c.EndByte()])
					}
				}
				// variable_declarator: name is the first child; jsx_attribute:
				// the attribute name (onClick).
				for i := 0; i < int(p.ChildCount()); i++ {
					ch := p.Child(i)
					if ch.Type() == "identifier" || ch.Type() == "property_identifier" {
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
	// Fallback for a declaration without a resolvable name. A byte-range key
	// changed with every edit above the node (a DELETED+ADDED pair each
	// time); walk's occurrence suffix keeps same-named fallbacks distinct.
	return "anon"
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

// isNamedExpression reports whether a function/class/arrow EXPRESSION has a
// stable name: a binding (hasNamedAssignment) or `export default`, which
// names the module's default export.
func isNamedExpression(n *sitter.Node) bool {
	if hasNamedAssignment(n) {
		return true
	}
	p := n.Parent()
	return p != nil && p.Type() == "export_statement"
}

// hasChildType reports whether n has a direct child of the given node type.
func hasChildType(n *sitter.Node, typ string) bool {
	for i := 0; i < int(n.ChildCount()); i++ {
		if n.Child(i).Type() == typ {
			return true
		}
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
	return hashNormalized(b, lang, nil, 0, true)
}

// spansWithin returns the sorted spans that overlap [a, b).
func spansWithin(spans []litSpan, a, b uint32) []litSpan {
	i := sort.Search(len(spans), func(i int) bool { return spans[i].end > a })
	j := i
	for j < len(spans) && spans[j].start < b {
		j++
	}
	return spans[i:j]
}

// hashNormalized hashes appendNormalized's output from a pooled buffer.
func hashNormalized(b []byte, lang Language, spans []litSpan, off uint32, lexical bool) string {
	bufPtr := hashBufPool.Get().(*[]byte)
	buf := (*bufPtr)[:0]
	if cap(buf) < len(b) {
		buf = make([]byte, 0, len(b))
	}
	defer func() {
		*bufPtr = buf
		hashBufPool.Put(bufPtr)
	}()

	buf = appendNormalized(buf, b, lang, spans, off, lexical)
	sum := sha256.Sum256(bytes.TrimSpace(buf))
	return hex.EncodeToString(sum[:])
}

// normalizeForHash strips comments and collapses whitespace so cosmetic-only
// edits (formatting, comment tweaks) do not register as semantic changes. It
// is the exact text hashNormalizedBodyBytes hashes.
func normalizeForHash(s string, lang Language) string {
	return string(bytes.TrimSpace(appendNormalizedBody(nil, []byte(s), lang)))
}

// appendNormalizedBody appends the normalized form of b to buf: comments
// stripped, whitespace runs collapsed to one space, string literals verbatim
// except for a CR before LF (Go raw strings, JS templates and Python source
// all read CRLF as LF, so a line-ending conversion is not a change).
//
// Python indentation is syntax, so there a newline outside brackets is not
// whitespace: each logical line is emitted on its own line prefixed by its
// block depth, computed like the tokenizer's INDENT/DEDENT stack. Moving a
// statement into or out of a block changes the hash; switching between 2- and
// 4-space indentation, blank lines and comment-only lines do not.
func appendNormalizedBody(buf, b []byte, lang Language) []byte {
	return appendNormalized(buf, b, lang, nil, 0, true)
}

// appendNormalized is appendNormalizedBody with a choice of where comments
// and literals come from. lexical finds them by scanning the bytes (generic
// parsers have no tree). Otherwise spans -- the grammar's own comment and
// literal ranges, in source coordinates, with b starting at off -- are
// authoritative and the bytes are never re-lexed, so a regex literal or a
// nested template cannot be mistaken for a comment or an open string. Both
// modes produce identical output wherever the lexer reads the code correctly.
func appendNormalized(buf, b []byte, lang Language, spans []litSpan, off uint32, lexical bool) []byte {
	py := lang == LangPython
	k := 0 // next candidate in spans
	prevSpace := false
	inLineComment := false
	inBlockComment := false
	inString := byte(0)

	lineStart := false // Python: at a line start, before its first token
	indentFrom := 0    // Python: where the current line's indentation begins
	depth := 0         // Python: open brackets (implicit line joining)
	var indents []int  // Python: widths of the enclosing indented blocks

	for i := 0; i < len(b); i++ {
		c := b[i]
		if inLineComment {
			if c == '\n' {
				inLineComment = false
				if py && depth == 0 {
					lineStart, indentFrom = true, i+1
				}
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
			if c == '\r' && i+1 < len(b) && b[i+1] == '\n' {
				continue
			}
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
		inSpan := false
		if !lexical {
			pos := off + uint32(i)
			for k < len(spans) && spans[k].end <= pos {
				k++
			}
			inSpan = k < len(spans) && spans[k].start <= pos
			if inSpan && spans[k].comment {
				end := min(int(spans[k].end-off), len(b))
				i = end - 1
				// The lexer consumes a line comment's newline without emitting
				// whitespace; do the same so both modes hash alike.
				if s := b[int(max(spans[k].start, off)-off):end]; bytes.HasPrefix(s, []byte("//")) || bytes.HasPrefix(s, []byte("#")) {
					j := end
					if j < len(b) && b[j] == '\r' {
						j++
					}
					if j < len(b) && b[j] == '\n' {
						i = j
						if py && depth == 0 {
							lineStart, indentFrom = true, j+1
						}
					}
				}
				continue
			}
		}
		if py && lineStart {
			switch c {
			case ' ', '\t':
				continue
			case '\r', '\n':
				indentFrom = i + 1 // blank line
				continue
			case '#':
				if lexical {
					inLineComment = true // comment-only line
					continue
				}
			}
			lineStart = false
			width := 0
			for _, w := range b[indentFrom:i] {
				if w == '\t' {
					width += 8 - width%8
				} else {
					width++
				}
			}
			for len(indents) > 0 && width < indents[len(indents)-1] {
				indents = indents[:len(indents)-1]
			}
			if len(indents) == 0 || width > indents[len(indents)-1] {
				indents = append(indents, width)
			}
			buf = bytes.TrimRight(buf, " ")
			buf = append(buf, '\n')
			for range indents {
				buf = append(buf, '\t')
			}
			prevSpace = true
		}
		if inSpan {
			// A grammar literal: verbatim, minus CR before LF.
			end := min(int(spans[k].end-off), len(b))
			for ; i < end; i++ {
				if b[i] == '\r' && i+1 < len(b) && b[i+1] == '\n' {
					continue
				}
				buf = append(buf, b[i])
			}
			i--
			prevSpace = false
			continue
		}
		switch c {
		case '/':
			if lexical && lang != LangPython && i+1 < len(b) && b[i+1] == '/' {
				inLineComment = true
				i++
				continue
			}
			if lexical && lang != LangPython && i+1 < len(b) && b[i+1] == '*' {
				inBlockComment = true
				i++
				continue
			}
		case '#':
			// Python-only line comment. In JS/TS '#' starts a private field
			// (this.#x) — stripping it there corrupts the hash.
			if lexical && lang == LangPython {
				inLineComment = true
				continue
			}
		case '"', '\'', '`':
			if lexical {
				inString = c
				buf = append(buf, c)
				prevSpace = false
				continue
			}
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
		case '\n':
			// A newline ends a logical line unless brackets or a trailing
			// backslash join it to the next one.
			if t := bytes.TrimRight(buf, " "); py && depth == 0 && (len(t) == 0 || t[len(t)-1] != '\\') {
				lineStart, indentFrom = true, i+1
				continue
			}
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
	return buf
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
