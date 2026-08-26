package ast

import (
	"bytes"
	"compress/flate"
	"container/list"
	"io"
	"os"
	"strconv"
	"sync"
)

// Retained source text dominates the engine's resident set: PrimeDirectory
// parses every eligible file in a workspace and each snapshot used to pin its
// full source string for the lifetime of the daemon. A 20k-file monorepo cost
// hundreds of megabytes that were only ever read again on the next edit of
// that one file.
//
// Two mechanisms bound it:
//
//  1. Cached source is DEFLATE-compressed. Source code compresses ~4x, and the
//     cost is paid once per parse (which already ran Tree-sitter over the same
//     bytes) instead of on every read.
//  2. A byte budget with LRU eviction. Past the budget the coldest snapshots
//     give up their compressed source while KEEPING their node map, so
//     signature/hash-level diffing stays exact and only the line-level
//     diff_snippet degrades for files nobody has touched in a long time.
const defaultSourceBudgetBytes = 48 << 20

// sourceBudgetBytes is the compressed-source ceiling for the snapshot cache.
// WRONGTRACE_AST_CACHE_MB overrides it; 0 disables source retention entirely
// (node-level diffs only, minimum footprint).
var sourceBudgetBytes = func() int64 {
	if v := os.Getenv("WRONGTRACE_AST_CACHE_MB"); v != "" {
		if mb, err := strconv.ParseInt(v, 10, 64); err == nil && mb >= 0 {
			return mb << 20
		}
	}
	return defaultSourceBudgetBytes
}()

var flateWriterPool = sync.Pool{
	New: func() any {
		w, err := flate.NewWriter(io.Discard, 1) // level 1: ~4x on source, near-memcpy speed
		if err != nil {
			return nil
		}
		return w
	},
}

// packSource compresses src for long-term retention. It returns nil when
// compression fails or does not pay for itself, in which case the caller keeps
// the plain bytes.
func packSource(src string) []byte {
	if src == "" {
		return nil
	}
	w, _ := flateWriterPool.Get().(*flate.Writer)
	if w == nil {
		return nil
	}
	defer flateWriterPool.Put(w)

	var buf bytes.Buffer
	buf.Grow(len(src) / 3)
	w.Reset(&buf)
	if _, err := io.WriteString(w, src); err != nil {
		return nil
	}
	if err := w.Close(); err != nil {
		return nil
	}
	if buf.Len() >= len(src) {
		return nil // incompressible; storing the original is cheaper
	}
	return buf.Bytes()
}

func unpackSource(packed []byte, size int) string {
	if len(packed) == 0 {
		return ""
	}
	r := flate.NewReader(bytes.NewReader(packed))
	defer r.Close()
	var out bytes.Buffer
	out.Grow(size)
	if _, err := io.Copy(&out, r); err != nil {
		return ""
	}
	return out.String()
}

// Source returns the file's full text, inflating the retained copy on demand.
// It returns "" once the snapshot's source has been evicted by the byte
// budget — callers must treat that as "no line-level diff available", never as
// "the file was empty".
func (f *FileSnapshot) Source() string {
	if f == nil {
		return ""
	}
	if f.RawContent != "" {
		return f.RawContent
	}
	return unpackSource(f.packed, f.rawLen)
}

// pack moves the snapshot's plain source into its compressed retention slot.
// Called exactly once, when the snapshot enters the cache.
func (f *FileSnapshot) pack() {
	if f == nil || f.RawContent == "" {
		return
	}
	f.rawLen = len(f.RawContent)
	if sourceBudgetBytes == 0 {
		f.RawContent = ""
		f.packed = nil
		return
	}
	if p := packSource(f.RawContent); p != nil {
		f.packed = p
		f.RawContent = ""
	}
}

// retainedBytes is the snapshot's contribution to the source budget.
func (f *FileSnapshot) retainedBytes() int64 {
	if f == nil {
		return 0
	}
	if len(f.packed) > 0 {
		return int64(len(f.packed))
	}
	return int64(len(f.RawContent))
}

// sourceLRU tracks snapshot recency so the cache can shed the coldest sources
// first. It holds paths only; the snapshots themselves live in Engine.snapshots.
type sourceLRU struct {
	order   *list.List               // front = most recently used
	entries map[string]*list.Element // path -> element
	bytes   int64
}

func newSourceLRU() *sourceLRU {
	return &sourceLRU{order: list.New(), entries: make(map[string]*list.Element)}
}

func (l *sourceLRU) touch(path string, delta int64) {
	if el, ok := l.entries[path]; ok {
		l.order.MoveToFront(el)
		l.bytes += delta
		return
	}
	l.entries[path] = l.order.PushFront(path)
	l.bytes += delta
}

func (l *sourceLRU) remove(path string, size int64) {
	if el, ok := l.entries[path]; ok {
		l.order.Remove(el)
		delete(l.entries, path)
		l.bytes -= size
	}
}

func (l *sourceLRU) reset() {
	l.order.Init()
	l.entries = make(map[string]*list.Element)
	l.bytes = 0
}

// evictTo drops retained source from the coldest snapshots until the budget is
// met. Node maps survive: a file whose source was evicted still diffs at
// signature and body-hash granularity on its next change.
func (l *sourceLRU) evictTo(budget int64, snapshots map[string]*FileSnapshot) {
	for l.bytes > budget {
		el := l.order.Back()
		if el == nil {
			l.bytes = 0
			return
		}
		path, _ := el.Value.(string)
		l.order.Remove(el)
		delete(l.entries, path)
		snap := snapshots[path]
		if snap == nil {
			continue
		}
		l.bytes -= snap.retainedBytes()
		snap.packed = nil
		snap.RawContent = ""
	}
	if l.bytes < 0 {
		l.bytes = 0
	}
}

// CachedSourceBytes reports the compressed source currently retained across all
// snapshots. Exposed for the /api/health footprint panel and for tests.
func (e *Engine) CachedSourceBytes() int64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.lru == nil {
		return 0
	}
	return e.lru.bytes
}
