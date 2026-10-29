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
	FilePath string
	Events   []Event
	NewSnap  *FileSnapshot
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
	// pay for inflating both sources.
	if prev != nil && next != nil {
		if prev.Hash != "" && next.Hash != "" && prev.Hash == next.Hash {
			res.NewSnap = next
			return res
		}
	}
	// Cached snapshots hold their source DEFLATE-compressed, so Source()
	// inflates the whole file. Resolve each side lazily, at most once, and only
	// when an emitted event actually needs a node body: an edit that changes
	// no declaration (comments, whitespace, imports) emits nothing and must
	// not pay for inflating either file. nodeBody slices these strings;
	// calling Source() per node would decompress the file once per declaration.
	//
	// There is deliberately no whole-file diff here. One used to be computed
	// on every save (an LCS over the changed region plus a 64 KiB snippet) and
	// nothing ever read it.
	prevSrc := lazySource(prev)
	nextSrc := lazySource(next)

	if prev == nil {
		for _, sig := range next.SortedSignatures() {
			n := next.Nodes[sig]
			diff, added, deleted := formatAddedDiff(nodeBody(nextSrc(), n))
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
		for _, sig := range prev.SortedSignatures() {
			n := prev.Nodes[sig]
			diff, added, deleted := formatDeletedDiff(nodeBody(prevSrc(), n))
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

	deleted := func(sig string, n Node) Event {
		diff, added, deleted := formatDeletedDiff(nodeBody(prevSrc(), n))
		return Event{
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
		}
	}
	added := func(sig string, n Node) Event {
		diff, added, deleted := formatAddedDiff(nodeBody(nextSrc(), n))
		return Event{
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
		}
	}
	modified := func(sig string, oldNode, newNode Node) Event {
		diff, added, deleted := generateLineDiff(nodeBody(prevSrc(), oldNode), nodeBody(nextSrc(), newNode))
		return Event{
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
		}
	}

	// Same-named declarations (several Go init funcs, getter/setter pairs)
	// are keyed base, base#2, base#3 in document order. Comparing those keys
	// one-to-one made the suffix positional: inserting one above the others
	// re-keyed every later one, reporting untouched bodies as MODIFIED. Each
	// base name's occurrences are aligned by body hash instead (unchanged
	// ones anchor), and only the gaps between anchors pair up positionally.
	prevGroups := groupByBase(prev)
	nextGroups := groupByBase(next)
	for base, ps := range prevGroups {
		if _, ok := nextGroups[base]; !ok {
			for _, sig := range ps {
				res.Events = append(res.Events, deleted(sig, prev.Nodes[sig]))
			}
		}
	}
	for base, ns := range nextGroups {
		for _, step := range alignOccurrences(prev, next, prevGroups[base], ns) {
			switch {
			case step.prev == "":
				res.Events = append(res.Events, added(step.next, next.Nodes[step.next]))
			case step.next == "":
				res.Events = append(res.Events, deleted(step.prev, prev.Nodes[step.prev]))
			default:
				if oldNode, newNode := prev.Nodes[step.prev], next.Nodes[step.next]; oldNode.Hash != newNode.Hash {
					res.Events = append(res.Events, modified(step.next, oldNode, newNode))
				}
			}
		}
	}

	// Stable ordering: deleted, modified, added; by signature within each,
	// as the per-signature loops used to emit them.
	sort.SliceStable(res.Events, func(i, j int) bool {
		ri, rj := actionRank(res.Events[i].Action), actionRank(res.Events[j].Action)
		if ri != rj {
			return ri < rj
		}
		return res.Events[i].Signature < res.Events[j].Signature
	})

	res.NewSnap = next
	return res
}

// occurrenceBase splits walk's occurrence suffix off a signature:
// "func:a.go::init#3" is occurrence 3 of "func:a.go::init".
func occurrenceBase(sig string) (string, int) {
	i := strings.LastIndexByte(sig, '#')
	if i <= 0 || i == len(sig)-1 {
		return sig, 1
	}
	n := 0
	for _, c := range sig[i+1:] {
		if c < '0' || c > '9' || n > 1<<20 {
			return sig, 1
		}
		n = n*10 + int(c-'0')
	}
	if n < 2 {
		return sig, 1
	}
	return sig[:i], n
}

// groupByBase lists each base signature's occurrences in document order.
func groupByBase(s *FileSnapshot) map[string][]string {
	type occ struct {
		sig string
		n   int
	}
	tmp := make(map[string][]occ, len(s.Nodes))
	for sig := range s.Nodes {
		base, n := occurrenceBase(sig)
		tmp[base] = append(tmp[base], occ{sig, n})
	}
	groups := make(map[string][]string, len(tmp))
	for base, os := range tmp {
		sort.Slice(os, func(i, j int) bool { return os[i].n < os[j].n })
		sigs := make([]string, len(os))
		for i, o := range os {
			sigs[i] = o.sig
		}
		groups[base] = sigs
	}
	return groups
}

// alignStep pairs a previous occurrence with a next one; an empty side means
// the occurrence was added (prev == "") or deleted (next == "").
type alignStep struct{ prev, next string }

// alignOccurrences matches one base name's occurrences across snapshots.
// Occurrences with identical bodies anchor (longest common subsequence over
// body hashes); between anchors the rest pair up in order, and any surplus
// is an addition or deletion.
func alignOccurrences(prev, next *FileSnapshot, ps, ns []string) []alignStep {
	if len(ps) <= 1 && len(ns) <= 1 {
		switch {
		case len(ps) == 0 && len(ns) == 0:
			return nil
		case len(ps) == 0:
			return []alignStep{{next: ns[0]}}
		case len(ns) == 0:
			return []alignStep{{prev: ps[0]}}
		}
		return []alignStep{{prev: ps[0], next: ns[0]}}
	}
	m, n := len(ps), len(ns)
	lcs := make([][]int, m+1)
	for i := range lcs {
		lcs[i] = make([]int, n+1)
	}
	for i := m - 1; i >= 0; i-- {
		for j := n - 1; j >= 0; j-- {
			if prev.Nodes[ps[i]].Hash == next.Nodes[ns[j]].Hash {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var steps []alignStep
	var gapP, gapN []string
	flush := func() {
		k := 0
		for ; k < len(gapP) && k < len(gapN); k++ {
			steps = append(steps, alignStep{prev: gapP[k], next: gapN[k]})
		}
		for _, p := range gapP[k:] {
			steps = append(steps, alignStep{prev: p})
		}
		for _, q := range gapN[k:] {
			steps = append(steps, alignStep{next: q})
		}
		gapP, gapN = gapP[:0], gapN[:0]
	}
	i, j := 0, 0
	for i < m && j < n {
		switch {
		case prev.Nodes[ps[i]].Hash == next.Nodes[ns[j]].Hash && lcs[i][j] == lcs[i+1][j+1]+1:
			flush()
			steps = append(steps, alignStep{prev: ps[i], next: ns[j]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			gapP = append(gapP, ps[i])
			i++
		default:
			gapN = append(gapN, ns[j])
			j++
		}
	}
	gapP = append(gapP, ps[i:]...)
	gapN = append(gapN, ns[j:]...)
	flush()
	return steps
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

// lazySource returns a memoized accessor for snap's source. Safe on a nil
// snapshot (Source is nil-safe).
func lazySource(snap *FileSnapshot) func() string {
	var (
		src    string
		loaded bool
	)
	return func() string {
		if !loaded {
			src = snap.Source()
			loaded = true
		}
		return src
	}
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
