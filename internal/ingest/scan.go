package ingest

import (
	"os"
	"strings"
	"time"
)

// The poll loop re-examines every watched agent directory on a fixed tick,
// forever. For a heavy Claude Code / Cursor user that is tens of thousands of
// transcript files, so the per-entry cost of the walk is the daemon's dominant
// steady-state CPU and allocation load.
//
// filepath.WalkDir imposes two costs this workload does not need:
//
//   - It sorts every directory's entries. Discovery does not care about order,
//     and sorting a session folder holding thousands of transcripts is pure
//     overhead on every tick.
//   - It builds the full path for EVERY entry before the callback can reject
//     it. Almost every entry is rejected on its name alone, so those were
//     allocations made only to be thrown away.
//
// The walk below reads directories unsorted, classifies by name before
// touching the path, and assembles candidate paths into a reused buffer. A
// steady-state poll -- the overwhelmingly common case, where nothing changed --
// therefore performs no allocation at all: Go compiles a map lookup keyed by
// string(byteSlice) without copying the bytes.
//
// File metadata comes from the directory entry rather than a separate stat.
// That matters most on Windows, where the directory enumeration already
// carries size and mtime and an explicit os.Stat per file costs an extra
// syscall each -- measured at roughly 8x the cost of the whole walk.

// Even a lean walk is dominated by the sheer size of the trees involved.
// Measured on a real workstation, ~/.wrongstack/projects alone holds 28,879
// files across 9,611 directories, and one steady-state poll -- a poll where
// nothing whatsoever had changed -- cost 450ms. Repeated every 25 seconds
// forever, that is a background process opening ten thousand directories a
// minute, which is exactly what "the machine always feels busy" is made of.
//
// So directories are tiered. Creating, renaming, or removing an entry moves
// its directory's mtime; only writing INTO an existing file does not. A
// directory whose mtime is unchanged AND whose newest transcript has been
// untouched for a day therefore has nothing a poll could learn, and is skipped
// after a single stat. Everything else is enumerated at full cadence.
//
// The one case this defers is an append to a transcript that has been dormant
// for over a day -- an agent resuming a day-old session in place. That is
// bounded by coldRescanEvery, so it surfaces within minutes rather than
// seconds. Discovery of NEW sessions is not affected at all: a new file moves
// its directory's mtime, which forces enumeration on the very next poll.

// maxScanDepth bounds how deep below a watched root the scan descends. Agent
// session layouts are shallow; anything deeper is a cache.
const maxScanDepth = 5

const (
	// dormantAfter is how long a directory's newest transcript must have gone
	// untouched before the directory is eligible to be skipped.
	dormantAfter = 24 * time.Hour
	// coldRescanEvery forces a full enumeration of even a dormant directory
	// every N polls, bounding how long an in-place append can hide.
	coldRescanEvery = 20
	// dirSettleWindow guards against coarse filesystem timestamp granularity
	// (FAT32 and some network mounts round to 2s). A directory touched within
	// this window is never trusted, so a change landing in the same timestamp
	// tick as the previous poll cannot be missed.
	dirSettleWindow = 2 * time.Second
)

// dirState is what a previous poll learned about one directory.
type dirState struct {
	modTime    time.Time // the directory's own mtime when it was enumerated
	newestFile time.Time // newest candidate-file mtime seen in it; zero if none
	subdirs    []string  // child directory names, ignore-filtered
	lastScan   uint64    // poll generation of the last full enumeration
	seen       uint64    // poll generation this entry was last reached
}

// fileKind classifies a discovered file into the parser that handles it.
type fileKind uint8

const (
	kindNone fileKind = iota
	kindJSONL
	kindJSON
	kindAider
)

// classifyLogFile decides how (and whether) a directory entry is parsed. It
// takes the base name plus its parent's base name so a caller can reject an
// entry BEFORE assembling its full path.
func classifyLogFile(name, parentName string) fileKind {
	// transcript_full.jsonl duplicates transcript.jsonl; the compact form wins.
	if name == "transcript_full.jsonl" {
		return kindNone
	}
	if strings.HasSuffix(name, ".jsonl") {
		return kindJSONL
	}
	if strings.HasSuffix(name, ".json") {
		// Only known task files (Cline/Roo and friends), never arbitrary JSON.
		switch parentName {
		case "tasks", "cline", "sessions", "conversations":
			return kindJSON
		}
		return kindNone
	}
	if name == ".aider.chat.history.md" {
		return kindAider
	}
	return kindNone
}

// baseName returns the last path segment without allocating.
func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// joinPath concatenates a directory and a child name with the platform
// separator. Unlike filepath.Join it does not Clean the result -- the inputs
// are already clean, since they come from a walk rooted at a cleaned path.
func joinPath(dir, name string) string {
	if dir == "" {
		return name
	}
	if c := dir[len(dir)-1]; c == '/' || c == '\\' {
		return dir + name
	}
	return dir + string(os.PathSeparator) + name
}

// appendPath writes dir + separator + name into buf, reusing its capacity.
func appendPath(buf []byte, dir, name string) []byte {
	buf = append(buf, dir...)
	if n := len(dir); n > 0 {
		if c := dir[n-1]; c != '/' && c != '\\' {
			buf = append(buf, os.PathSeparator)
		}
	}
	return append(buf, name...)
}

// readDirUnsorted lists a directory without the lexical sort os.ReadDir
// performs.
func readDirUnsorted(dir string) ([]os.DirEntry, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	entries, err := f.ReadDir(-1)
	_ = f.Close()
	return entries, err
}

// scanRoot walks one watched root and hands every transcript candidate to
// visit. Directories are traversed with an explicit stack; dormant ones cost a
// single stat instead of a full enumeration.
func (sw *SessionWatcher) scanRoot(root string, gen uint64, now time.Time) {
	type frame struct {
		dir   string
		depth int
	}
	stack := make([]frame, 1, 32)
	stack[0] = frame{dir: root}

	var pathBuf []byte
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		info, err := os.Stat(cur.dir)
		if err != nil || !info.IsDir() {
			continue
		}
		dirMod := info.ModTime()

		if subdirs, skip := sw.reuseDirState(cur.dir, dirMod, gen, now); skip {
			if cur.depth < maxScanDepth {
				for _, name := range subdirs {
					stack = append(stack, frame{dir: joinPath(cur.dir, name), depth: cur.depth + 1})
				}
			}
			continue
		}

		entries, err := readDirUnsorted(cur.dir)
		if err != nil {
			continue
		}
		parent := baseName(cur.dir)
		var subdirs []string
		var newestFile time.Time
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() {
				if !isIgnoredLogDir(name) {
					subdirs = append(subdirs, name)
				}
				continue
			}
			kind := classifyLogFile(name, parent)
			if kind == kindNone {
				continue
			}
			fi, iErr := entry.Info()
			if iErr != nil || fi == nil {
				continue
			}
			if mod := fi.ModTime(); mod.After(newestFile) {
				newestFile = mod
			}
			pathBuf = appendPath(pathBuf[:0], cur.dir, name)
			sw.visit(pathBuf, kind, fi.Size(), fi.ModTime())
		}

		sw.storeDirState(cur.dir, dirState{
			modTime:    dirMod,
			newestFile: newestFile,
			subdirs:    subdirs,
			lastScan:   gen,
			seen:       gen,
		})

		if cur.depth < maxScanDepth {
			for _, name := range subdirs {
				stack = append(stack, frame{dir: joinPath(cur.dir, name), depth: cur.depth + 1})
			}
		}
	}
}

// reuseDirState decides whether a directory can be served from the previous
// poll's knowledge. It returns the cached child directories and true when the
// directory needs no enumeration this round.
func (sw *SessionWatcher) reuseDirState(dir string, dirMod time.Time, gen uint64, now time.Time) ([]string, bool) {
	sw.dirMu.Lock()
	defer sw.dirMu.Unlock()

	st, ok := sw.dirCache[dir]
	if !ok {
		return nil, false
	}
	st.seen = gen
	sw.dirCache[dir] = st

	if !st.modTime.Equal(dirMod) {
		return nil, false // something was created, renamed, or removed
	}
	if now.Sub(dirMod) <= dirSettleWindow {
		return nil, false // timestamp may still be rounding
	}
	if !st.newestFile.IsZero() && now.Sub(st.newestFile) <= dormantAfter {
		return nil, false // still holds a recently active transcript
	}
	if gen-st.lastScan >= coldRescanEvery {
		return nil, false // forced periodic re-read
	}
	return st.subdirs, true
}

func (sw *SessionWatcher) storeDirState(dir string, st dirState) {
	sw.dirMu.Lock()
	if sw.dirCache == nil {
		sw.dirCache = make(map[string]dirState)
	}
	sw.dirCache[dir] = st
	sw.dirMu.Unlock()
}

// pruneDirCache drops directories the latest poll never reached, so the memo
// cannot outlive the trees it describes.
func (sw *SessionWatcher) pruneDirCache(gen uint64) {
	sw.dirMu.Lock()
	defer sw.dirMu.Unlock()
	for dir, st := range sw.dirCache {
		if st.seen != gen {
			delete(sw.dirCache, dir)
		}
	}
}

// visit is the per-candidate hot path. It answers "has this transcript grown?"
// without materializing the path as a string: string(pathBuf) in a map index
// expression is compiled to a no-copy lookup. Only a file that actually needs
// work pays for the allocation.
func (sw *SessionWatcher) visit(pathBuf []byte, kind fileKind, size int64, modTime time.Time) {
	sw.mu.Lock()
	st, seen := sw.seenFiles[string(pathBuf)]
	unchanged := seen && size <= st.offset && !modTime.After(st.modTime)
	sw.mu.Unlock()
	if unchanged {
		return
	}
	sw.processFile(string(pathBuf), kind, size, modTime)
}
