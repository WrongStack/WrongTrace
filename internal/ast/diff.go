package ast

import (
	"sort"
	"strings"
	"sync"
	"time"
)

var dpPool = sync.Pool{
	New: func() any {
		s := make([]int, 0, 16384)
		return &s
	},
}

const (
	maxDiffSnippetBytes = 64 * 1024
	diffContextLines    = 3
)

type boundedDiffBuilder struct {
	b         strings.Builder
	first     bool
	bounded   bool
	truncated bool
}

func (b *boundedDiffBuilder) emit(prefix, line string) {
	if b.truncated {
		return
	}
	required := len(prefix) + len(line)
	if !b.first {
		required++
	}
	// Leave room for the truncation marker so the persisted/WebSocket payload
	// always remains below the advertised cap.
	const marker = "\n  … diff snippet truncated …"
	if b.bounded && b.b.Len()+required+len(marker) > maxDiffSnippetBytes {
		b.truncated = true
		return
	}
	if !b.first {
		b.b.WriteByte('\n')
	}
	b.first = false
	b.b.WriteString(prefix)
	b.b.WriteString(line)
}

func (b *boundedDiffBuilder) String() string {
	if b.truncated {
		b.b.WriteString("\n  … diff snippet truncated …")
	}
	return b.b.String()
}

// Action is the lifecycle state transition produced by a semantic diff.
type Action string

const (
	ActionAdded    Action = "ADDED"
	ActionModified Action = "MODIFIED"
	ActionDeleted  Action = "DELETED"
)

// Event is the persisted record of one AST node transition. RunID may be empty
// when the change could not be correlated to an active agent run; the engine
// backfills it during the correlation window.
type Event struct {
	RunID        string    `json:"run_id"`
	RepoName     string    `json:"repo_name"`
	FilePath     string    `json:"file_path"`
	Signature    string    `json:"node_signature"`
	NodeType     NodeKind  `json:"node_type"`
	Action       Action    `json:"action"`
	BodyHash     string    `json:"ast_content_hash"`
	LOC          int       `json:"lines_of_code"`
	StartLine    uint32    `json:"start_line"`
	EndLine      uint32    `json:"end_line"`
	DiffSnippet  string    `json:"diff_snippet"`
	AddedLines   int       `json:"added_lines"`
	DeletedLines int       `json:"deleted_lines"`
	OccurredAt   time.Time `json:"event_time"`
}

// DiffResult is the ordered set of Events emitted for one file transition.
// The order is stable: DELETED, MODIFIED, ADDED — which matches what
// downstream consumers (DB writers, websocket hub) prefer for display.
type DiffResult struct {
	FilePath    string
	Events      []Event
	NewSnap     *FileSnapshot
	FileDiff    string // Unified full-file diff snippet
	FileAdded   int    // Total lines added in the whole file
	FileDeleted int    // Total lines deleted in the whole file
}

// Diff computes the semantic delta between a previous snapshot (possibly nil)
// and a freshly-parsed snapshot. It returns ADDED/MODIFIED/DELETED events for
// every node that transitioned. A nil prev produces only ADDED events.
func Diff(repoName string, prev, next *FileSnapshot) DiffResult {
	res := DiffResult{FilePath: safePath(next, prev)}
	now := time.Now().UTC()

	if prev == nil && next == nil {
		return res
	}

	// Cheap identity check first: comparing the retained file hashes must not
	// pay for inflating both sources. Cached snapshots hold their source
	// DEFLATE-compressed, so the two Source() calls below each decompress the
	// whole file — wasted work whenever the hash shortcut fires, which is the
	// common case for no-op touches. nodeBody slices these strings; calling
	// Source() per node would decompress the file once per declaration.
	if prev != nil && next != nil {
		if prev.Hash != "" && next.Hash != "" && prev.Hash == next.Hash {
			res.NewSnap = next
			return res
		}
	}
	prevSrc := prev.Source()
	nextSrc := next.Source()

	if prev == nil {
		if nextSrc != "" {
			fileDiff, fileAdded, fileDeleted := formatAddedDiff(nextSrc)
			res.FileDiff = fileDiff
			res.FileAdded = fileAdded
			res.FileDeleted = fileDeleted
		}

		for _, sig := range next.SortedSignatures() {
			n := next.Nodes[sig]
			diff, added, deleted := formatAddedDiff(nodeBody(nextSrc, n))
			res.Events = append(res.Events, Event{
				RepoName:     repoName,
				FilePath:     next.Path,
				Signature:    sig,
				NodeType:     n.Kind,
				Action:       ActionAdded,
				BodyHash:     n.Hash,
				LOC:          n.LOC,
				StartLine:    n.StartLine,
				EndLine:      n.EndLine,
				DiffSnippet:  diff,
				AddedLines:   added,
				DeletedLines: deleted,
				OccurredAt:   now,
			})
		}

		res.NewSnap = next
		return res
	}
	if next == nil {
		if prevSrc != "" {
			fileDiff, fileAdded, fileDeleted := formatDeletedDiff(prevSrc)
			res.FileDiff = fileDiff
			res.FileAdded = fileAdded
			res.FileDeleted = fileDeleted
		}

		for _, sig := range prev.SortedSignatures() {
			n := prev.Nodes[sig]
			diff, added, deleted := formatDeletedDiff(nodeBody(prevSrc, n))
			res.Events = append(res.Events, Event{
				RepoName:     repoName,
				FilePath:     prev.Path,
				Signature:    sig,
				NodeType:     n.Kind,
				Action:       ActionDeleted,
				BodyHash:     n.Hash,
				LOC:          n.LOC,
				StartLine:    n.StartLine,
				EndLine:      n.EndLine,
				DiffSnippet:  diff,
				AddedLines:   added,
				DeletedLines: deleted,
				OccurredAt:   now,
			})
		}

		return res
	}

	if prevSrc != "" || nextSrc != "" {
		fileDiff, fileAdded, fileDeleted := generateLineDiff(prevSrc, nextSrc)
		res.FileDiff = fileDiff
		res.FileAdded = fileAdded
		res.FileDeleted = fileDeleted
	}

	prevSigs := prev.SortedSignatures()
	nextSigs := next.SortedSignatures()

	// DELETED first — anything in prev but not in next.
	prevSet := sigSet(prevSigs)
	nextSet := sigSet(nextSigs)
	for _, sig := range prevSigs {
		if _, ok := nextSet[sig]; !ok {
			n := prev.Nodes[sig]
			diff, added, deleted := formatDeletedDiff(nodeBody(prevSrc, n))
			res.Events = append(res.Events, Event{
				RepoName:     repoName,
				FilePath:     prev.Path,
				Signature:    sig,
				NodeType:     n.Kind,
				Action:       ActionDeleted,
				BodyHash:     n.Hash,
				LOC:          n.LOC,
				StartLine:    n.StartLine,
				EndLine:      n.EndLine,
				DiffSnippet:  diff,
				AddedLines:   added,
				DeletedLines: deleted,
				OccurredAt:   now,
			})
		}
	}

	// MODIFIED — same signature, hash changed.
	// ADDED — new signature.
	for _, sig := range nextSigs {
		newNode := next.Nodes[sig]
		if _, existed := prevSet[sig]; !existed {
			diff, added, deleted := formatAddedDiff(nodeBody(nextSrc, newNode))
			res.Events = append(res.Events, Event{
				RepoName:     repoName,
				FilePath:     next.Path,
				Signature:    sig,
				NodeType:     newNode.Kind,
				Action:       ActionAdded,
				BodyHash:     newNode.Hash,
				LOC:          newNode.LOC,
				StartLine:    newNode.StartLine,
				EndLine:      newNode.EndLine,
				DiffSnippet:  diff,
				AddedLines:   added,
				DeletedLines: deleted,
				OccurredAt:   now,
			})
			continue
		}
		oldNode := prev.Nodes[sig]
		if oldNode.Hash != newNode.Hash {
			diff, added, deleted := generateLineDiff(nodeBody(prevSrc, oldNode), nodeBody(nextSrc, newNode))
			res.Events = append(res.Events, Event{
				RepoName:     repoName,
				FilePath:     next.Path,
				Signature:    sig,
				NodeType:     newNode.Kind,
				Action:       ActionModified,
				BodyHash:     newNode.Hash,
				LOC:          newNode.LOC,
				StartLine:    newNode.StartLine,
				EndLine:      newNode.EndLine,
				DiffSnippet:  diff,
				AddedLines:   added,
				DeletedLines: deleted,
				OccurredAt:   now,
			})
		}
	}

	// Stable ordering: deleted, modified, added.
	sort.SliceStable(res.Events, func(i, j int) bool {
		return actionRank(res.Events[i].Action) < actionRank(res.Events[j].Action)
	})

	res.NewSnap = next
	return res
}

// nodeBody resolves a native parser node directly from the snapshot's single
// raw source allocation. Generic parsers and hand-built test snapshots retain
// Body as a compatibility fallback. This avoids keeping a second string copy
// for every function/class in large workspaces.
func nodeBody(src string, n Node) string {
	if n.EndByte > n.StartByte && n.EndByte <= uint32(len(src)) {
		return src[n.StartByte:n.EndByte]
	}
	return n.Body
}

func actionRank(a Action) int {
	switch a {
	case ActionDeleted:
		return 0
	case ActionModified:
		return 1
	case ActionAdded:
		return 2
	}
	return 99
}

func sigSet(sigs []string) map[string]struct{} {
	out := make(map[string]struct{}, len(sigs))
	for _, s := range sigs {
		out[s] = struct{}{}
	}
	return out
}

func safePath(a, b *FileSnapshot) string {
	if a != nil {
		return a.Path
	}
	if b != nil {
		return b.Path
	}
	return ""
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	// Fast path: avoid full-string ReplaceAll allocation when no \r is present
	if !strings.Contains(s, "\r") {
		s = strings.TrimSuffix(s, "\n")
		if s == "" {
			return nil
		}
		return strings.Split(s, "\n")
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func formatAddedDiff(body string) (string, int, int) {
	lines := splitLines(body)
	if len(lines) == 0 {
		return "", 0, 0
	}
	var b boundedDiffBuilder
	b.first = true
	estimated := len(body) + len(lines)*3
	b.bounded = estimated > maxDiffSnippetBytes
	b.b.Grow(min(estimated, maxDiffSnippetBytes))
	for _, l := range lines {
		b.emit("+ ", l)
	}
	return b.String(), len(lines), 0
}

func formatDeletedDiff(body string) (string, int, int) {
	lines := splitLines(body)
	if len(lines) == 0 {
		return "", 0, 0
	}
	var b boundedDiffBuilder
	b.first = true
	estimated := len(body) + len(lines)*3
	b.bounded = estimated > maxDiffSnippetBytes
	b.b.Grow(min(estimated, maxDiffSnippetBytes))
	for _, l := range lines {
		b.emit("- ", l)
	}
	return b.String(), 0, len(lines)
}

func generateLineDiff(oldText, newText string) (string, int, int) {
	if oldText == newText {
		return "", 0, 0
	}
	oldLines := splitLines(oldText)
	newLines := splitLines(newText)

	if len(oldLines) == 0 && len(newLines) == 0 {
		return "", 0, 0
	}
	if len(oldLines) == 0 {
		return formatAddedDiff(newText)
	}
	if len(newLines) == 0 {
		return formatDeletedDiff(oldText)
	}

	var b boundedDiffBuilder
	b.first = true
	added := 0
	deleted := 0

	// Decide the byte cap BEFORE the first emit. emit() only enforces the cap
	// while b.bounded is true, so leaving this until after the common-prefix
	// context below let those writes grow the builder past
	// maxDiffSnippetBytes; the capacity arithmetic further down then
	// subtracts past zero and panics inside Grow. Setting it up front also
	// keeps the snippet inside the cap advertised to the DB and WebSocket
	// payload. Same ordering as formatAddedDiff and formatDeletedDiff.
	estimated := len(oldText) + len(newText) + (len(oldLines)+len(newLines))*3
	b.bounded = estimated > maxDiffSnippetBytes

	// 1. Strip the common prefix. Only nearby context belongs in a snippet;
	// emitting an unchanged 20k-line prefix made a one-line edit allocate and
	// persist almost the entire file.
	prefixLen := 0
	for prefixLen < len(oldLines) && prefixLen < len(newLines) && oldLines[prefixLen] == newLines[prefixLen] {
		prefixLen++
	}
	prefixStart := max(0, prefixLen-diffContextLines)
	if prefixStart > 0 {
		b.emit("  ", "… unchanged lines omitted …")
	}
	for i := prefixStart; i < prefixLen; i++ {
		b.emit("  ", oldLines[i])
	}

	// 2. Strip common suffix
	suffixLen := 0
	for suffixLen < (len(oldLines)-prefixLen) && suffixLen < (len(newLines)-prefixLen) &&
		oldLines[len(oldLines)-1-suffixLen] == newLines[len(newLines)-1-suffixLen] {
		suffixLen++
	}

	midOld := oldLines[prefixLen : len(oldLines)-suffixLen]
	midNew := newLines[prefixLen : len(newLines)-suffixLen]

	m, n := len(midOld), len(midNew)
	grow := 4 * 1024
	if m*n > 200000 {
		grow = min(estimated, maxDiffSnippetBytes)
	}
	b.b.Grow(min(grow, maxDiffSnippetBytes-b.b.Len()))

	// Memory guard: for huge un-aligned diffs (> 200,000 cells), emit straightforward deletion then addition
	if m*n > 200000 {
		for _, l := range midOld {
			b.emit("- ", l)
			deleted++
		}
		for _, l := range midNew {
			b.emit("+ ", l)
			added++
		}
	} else if m > 0 || n > 0 {
		type match struct {
			oldIdx, newIdx int
		}

		stride := n + 1
		reqSize := (m + 1) * stride
		bufPtr := dpPool.Get().(*[]int)
		if cap(*bufPtr) < reqSize {
			*bufPtr = make([]int, reqSize)
		} else {
			*bufPtr = (*bufPtr)[:reqSize]
			for k := range *bufPtr {
				(*bufPtr)[k] = 0
			}
		}
		dp := *bufPtr
		defer dpPool.Put(bufPtr)

		for i := 1; i <= m; i++ {
			row := i * stride
			prevRow := (i - 1) * stride
			for j := 1; j <= n; j++ {
				if midOld[i-1] == midNew[j-1] {
					dp[row+j] = dp[prevRow+j-1] + 1
				} else if dp[prevRow+j] >= dp[row+j-1] {
					dp[row+j] = dp[prevRow+j]
				} else {
					dp[row+j] = dp[row+j-1]
				}
			}
		}

		var matches []match
		i, j := m, n
		for i > 0 && j > 0 {
			row := i * stride
			prevRow := (i - 1) * stride
			if midOld[i-1] == midNew[j-1] {
				matches = append(matches, match{oldIdx: i - 1, newIdx: j - 1})
				i--
				j--
			} else if dp[prevRow+j] >= dp[row+j-1] {
				i--
			} else {
				j--
			}
		}
		for l, r := 0, len(matches)-1; l < r; l, r = l+1, r-1 {
			matches[l], matches[r] = matches[r], matches[l]
		}

		currOld, currNew := 0, 0
		for _, mat := range matches {
			for currOld < mat.oldIdx {
				b.emit("- ", midOld[currOld])
				deleted++
				currOld++
			}
			for currNew < mat.newIdx {
				b.emit("+ ", midNew[currNew])
				added++
				currNew++
			}
			b.emit("  ", midOld[currOld])
			currOld++
			currNew++
		}
		for currOld < len(midOld) {
			b.emit("- ", midOld[currOld])
			deleted++
			currOld++
		}
		for currNew < len(midNew) {
			b.emit("+ ", midNew[currNew])
			added++
			currNew++
		}
	}

	// 3. Emit only nearby common suffix context.
	suffixStart := len(oldLines) - suffixLen
	suffixEnd := min(len(oldLines), suffixStart+diffContextLines)
	for s := suffixStart; s < suffixEnd; s++ {
		b.emit("  ", oldLines[s])
	}
	if suffixLen > diffContextLines {
		b.emit("  ", "… unchanged lines omitted …")
	}

	return b.String(), added, deleted
}
