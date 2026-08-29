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
	parser := e.parserFor(lang)
	if parser == nil {
		// Generic heuristic parser for languages without native tree-sitter C grammar
		return parseGenericSource(path, src, lang), nil
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
		Hash:       hashBytes(src),
		RawContent: raw,
		LOC:        strings.Count(raw, "\n") + 1,
	}
	collectNodes(root, src, lang, filepath.Base(path), snap)
	snap.SortedSignatures()
	return snap, nil
}

// parseGenericSource extracts declarations using robust pattern matching for languages without a compiled Tree-sitter grammar.
func parseGenericSource(path string, src []byte, lang Language) *FileSnapshot {
	raw := string(src)
	lines := strings.Split(raw, "\n")
	base := filepath.Base(path)
	snap := &FileSnapshot{
		Path:       path,
		Nodes:      map[string]Node{},
		Hash:       hashBytes(src),
		RawContent: raw,
		LOC:        len(lines),
	}

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "#") {
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
					name = strings.Split(parts[j+1], "(")[0]
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
		case strings.HasPrefix(trimmed, "class ") || strings.Contains(trimmed, " class "):
			kind = NodeClass
			parts := strings.Fields(trimmed)
			for j, p := range parts {
				if p == "class" && j+1 < len(parts) {
					name = strings.Split(parts[j+1], "{")[0]
					name = strings.Split(name, "(")[0]
					break
				}
			}
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
			}
		}

		if name != "" && kind != "" {
			name = strings.TrimSpace(name)
			sig := fmt.Sprintf("%s:%s::%s", kind, base, name)

			startLine := i + 1
			endLine := i + 1
			var bodyLines []string
			bodyLines = append(bodyLines, line)

			if strings.Contains(line, "{") {
				braceCount := countCodeBraces(line)
				j := i + 1
				for ; j < len(lines) && braceCount > 0; j++ {
					l := lines[j]
					bodyLines = append(bodyLines, l)
					braceCount += countCodeBraces(l)
				}
				endLine = j
			}

			fullBody := strings.Join(bodyLines, "\n")
			nodeHash := hashNormalizedBody(fullBody, lang)

			snap.Nodes[sig] = Node{
				Signature: sig,
				Kind:      kind,
				Body:      fullBody,
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

// countCodeBraces returns the net delta of '{' minus '}', safely ignoring braces inside strings and comments.
func countCodeBraces(s string) int {
	net := 0
	inString := byte(0)
	inLineComment := false
	inBlockComment := false

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
			if i+1 < len(s) && s[i+1] == '/' {
				inLineComment = true
				i++
				continue
			}
			if i+1 < len(s) && s[i+1] == '*' {
				inBlockComment = true
				i++
				continue
			}
		case '#':
			inLineComment = true
			continue
		case '"', '\'', '`':
			inString = c
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
			if i+1 < len(b) && b[i+1] == '/' {
				inLineComment = true
				i++
				continue
			}
			if i+1 < len(b) && b[i+1] == '*' {
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
			if i+1 < len(s) && s[i+1] == '/' {
				inLineComment = true
				i++
				continue
			}
			if i+1 < len(s) && s[i+1] == '*' {
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
func (s *FileSnapshot) SortedSignatures() []string {
	if s == nil {
		return nil
	}
	if s.sortedSigs != nil && len(s.sortedSigs) == len(s.Nodes) {
		return s.sortedSigs
	}
	out := make([]string, 0, len(s.Nodes))
	for k := range s.Nodes {
		out = append(out, k)
	}
	sort.Strings(out)
	s.sortedSigs = out
	return out
}
