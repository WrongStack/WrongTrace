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
	if prev == nil {
		if next.RawContent != "" {
			fileDiff, fileAdded, fileDeleted := formatAddedDiff(next.RawContent)
			res.FileDiff = fileDiff
			res.FileAdded = fileAdded
			res.FileDeleted = fileDeleted
		}

		for _, sig := range next.SortedSignatures() {
			n := next.Nodes[sig]
			diff, added, deleted := formatAddedDiff(n.Body)
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
		if prev.RawContent != "" {
			fileDiff, fileAdded, fileDeleted := formatDeletedDiff(prev.RawContent)
			res.FileDiff = fileDiff
			res.FileAdded = fileAdded
			res.FileDeleted = fileDeleted
		}

		for _, sig := range prev.SortedSignatures() {
			n := prev.Nodes[sig]
			diff, added, deleted := formatDeletedDiff(n.Body)
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

	if prev.Hash != "" && next.Hash != "" && prev.Hash == next.Hash {
		res.NewSnap = next
		return res
	}

	if prev.RawContent != "" || next.RawContent != "" {
		fileDiff, fileAdded, fileDeleted := generateLineDiff(prev.RawContent, next.RawContent)
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
			diff, added, deleted := formatDeletedDiff(n.Body)
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
			diff, added, deleted := formatAddedDiff(newNode.Body)
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
			diff, added, deleted := generateLineDiff(oldNode.Body, newNode.Body)
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
	var b strings.Builder
	b.Grow(len(body) + len(lines)*3)
	for i, l := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("+ ")
		b.WriteString(l)
	}
	return b.String(), len(lines), 0
}

func formatDeletedDiff(body string) (string, int, int) {
	lines := splitLines(body)
	if len(lines) == 0 {
		return "", 0, 0
	}
	var b strings.Builder
	b.Grow(len(body) + len(lines)*3)
	for i, l := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("- ")
		b.WriteString(l)
	}
	return b.String(), 0, len(lines)
}

func generateLineDiff(oldText, newText string) (string, int, int) {
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

	var b strings.Builder
	b.Grow(len(oldText) + len(newText) + (len(oldLines)+len(newLines))*3)
	added := 0
	deleted := 0

	first := true
	emit := func(prefix, line string) {
		if !first {
			b.WriteByte('\n')
		}
		first = false
		b.WriteString(prefix)
		b.WriteString(line)
	}

	// 1. Strip and emit common prefix
	prefixLen := 0
	for prefixLen < len(oldLines) && prefixLen < len(newLines) && oldLines[prefixLen] == newLines[prefixLen] {
		emit("  ", oldLines[prefixLen])
		prefixLen++
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

	// Memory guard: for huge un-aligned diffs (> 200,000 cells), emit straightforward deletion then addition
	if m*n > 200000 {
		for _, l := range midOld {
			emit("- ", l)
			deleted++
		}
		for _, l := range midNew {
			emit("+ ", l)
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
				emit("- ", midOld[currOld])
				deleted++
				currOld++
			}
			for currNew < mat.newIdx {
				emit("+ ", midNew[currNew])
				added++
				currNew++
			}
			emit("  ", midOld[currOld])
			currOld++
			currNew++
		}
		for currOld < len(midOld) {
			emit("- ", midOld[currOld])
			deleted++
			currOld++
		}
		for currNew < len(midNew) {
			emit("+ ", midNew[currNew])
			added++
			currNew++
		}
	}

	// 3. Emit common suffix
	for s := len(oldLines) - suffixLen; s < len(oldLines); s++ {
		emit("  ", oldLines[s])
	}

	return b.String(), added, deleted
}
