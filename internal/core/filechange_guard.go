package core

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// maxParseFileBytes is the single size ceiling for AST parsing. The startup
// indexer (PrimeDirectory), the parse-eligibility predicate, and the live
// change handler used three different limits (1MB, 4MB, 5MB): a file between
// the indexer's limit and the handler's was never baselined at startup but
// was parsed on its first edit, so that edit diffed against "no snapshot" and
// reported every declaration in the file as ADDED. One constant keeps the
// three gates in agreement.
const maxParseFileBytes = 1 << 20

// newFileWindow bounds how recently a file must have been created (by its
// filesystem birth time) for a first sighting without a cached snapshot to
// count as a genuine creation that deserves ADDED events. The watcher debounce
// is far below this; anything older is a pre-existing file the cache simply
// had not seen (non-active project, post-Reset, LRU eviction).
const newFileWindow = 2 * time.Minute

// tombstoneTTL / maxTombstones bound the memory of "this path was deleted
// in-session" markers that let a delete-then-recreate emit ADDED even when
// the OS preserves the old birth time (Windows file-system tunneling).
const (
	tombstoneTTL  = 10 * time.Minute
	maxTombstones = 4096
)

// fileBirthTimeHook is the birth-time source; tests replace it to make the
// creation heuristic deterministic across filesystems.
var fileBirthTimeHook = fileBirthTime

// keyedMutex serializes work per key (a file path). Entries are reference
// counted and dropped as soon as no goroutine holds or waits on them, so the
// map is bounded by the number of in-flight operations, not by the number of
// paths ever seen.
type keyedMutex struct {
	mu sync.Mutex
	m  map[string]*keyedEntry
}

type keyedEntry struct {
	mu   sync.Mutex
	refs int
}

// lock acquires the mutex for key and returns its release function.
func (k *keyedMutex) lock(key string) func() {
	k.mu.Lock()
	if k.m == nil {
		k.m = make(map[string]*keyedEntry)
	}
	ent := k.m[key]
	if ent == nil {
		ent = &keyedEntry{}
		k.m[key] = ent
	}
	ent.refs++
	k.mu.Unlock()

	ent.mu.Lock()
	return func() {
		ent.mu.Unlock()
		k.mu.Lock()
		ent.refs--
		if ent.refs == 0 {
			delete(k.m, key)
		}
		k.mu.Unlock()
	}
}

// size reports the number of live entries (tests assert boundedness).
func (k *keyedMutex) size() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.m)
}

func tombstoneKey(path string) string {
	return strings.ToLower(filepath.Clean(path))
}

// recordTombstone remembers that path was deleted while its symbols were
// cached (DELETED events were emitted), so a re-creation reports ADDED.
func (e *Engine) recordTombstone(path string, now time.Time) {
	e.tombMu.Lock()
	defer e.tombMu.Unlock()
	if e.tombstones == nil {
		e.tombstones = make(map[string]time.Time)
	}
	if len(e.tombstones) >= maxTombstones {
		cutoff := now.Add(-tombstoneTTL)
		for k, at := range e.tombstones {
			if at.Before(cutoff) {
				delete(e.tombstones, k)
			}
		}
		if len(e.tombstones) >= maxTombstones {
			// Still full of fresh markers: dropping them all only degrades a
			// later re-create to a silent baseline, never to a false ADDED.
			e.tombstones = make(map[string]time.Time)
		}
	}
	e.tombstones[tombstoneKey(path)] = now
}

// consumeTombstone reports (and clears) a fresh deletion marker for path.
func (e *Engine) consumeTombstone(path string, now time.Time) bool {
	e.tombMu.Lock()
	defer e.tombMu.Unlock()
	key := tombstoneKey(path)
	at, ok := e.tombstones[key]
	if !ok {
		return false
	}
	delete(e.tombstones, key)
	return now.Sub(at) <= tombstoneTTL
}

// firstSightIsCreation decides whether a file with no cached snapshot is a
// genuine in-session creation (emit ADDED for its declarations) or a
// pre-existing file the cache had not seen (store a silent baseline). The
// watcher hands the engine only a path -- CREATE and WRITE are debounced into
// the same HandleFileChange call -- so the engine infers creation from an
// in-session deletion marker or from a recent filesystem birth time. Where
// the birth time is unavailable the answer is "baseline": a missed ADDED is
// recoverable noise, a flood of false ADDED events corrupts churn analytics.
func (e *Engine) firstSightIsCreation(path string, info os.FileInfo) bool {
	now := time.Now()
	if e.consumeTombstone(path, now) {
		return true
	}
	birth, ok := fileBirthTimeHook(path, info)
	if !ok || birth.IsZero() {
		return false
	}
	return now.Sub(birth) <= newFileWindow
}
