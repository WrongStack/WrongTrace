package ast

import (
	"sort"
	"time"
)

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
	RunID      string
	RepoName   string
	FilePath   string
	Signature  string
	NodeType   NodeKind
	Action     Action
	BodyHash   string
	LOC        int
	OccurredAt time.Time
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
	if prev == nil {
		for _, sig := range next.SortedSignatures() {
			n := next.Nodes[sig]
			res.Events = append(res.Events, Event{
				RepoName:   repoName,
				FilePath:   next.Path,
				Signature:  sig,
				NodeType:   n.Kind,
				Action:     ActionAdded,
				BodyHash:   n.Hash,
				LOC:        n.LOC,
				OccurredAt: now,
			})
		}
		res.NewSnap = next
		return res
	}
	if next == nil {
		for _, sig := range prev.SortedSignatures() {
			n := prev.Nodes[sig]
			res.Events = append(res.Events, Event{
				RepoName:   repoName,
				FilePath:   prev.Path,
				Signature:  sig,
				NodeType:   n.Kind,
				Action:     ActionDeleted,
				BodyHash:   n.Hash,
				LOC:        n.LOC,
				OccurredAt: now,
			})
		}
		return res
	}

	prevSigs := prev.SortedSignatures()
	nextSigs := next.SortedSignatures()

	// DELETED first — anything in prev but not in next.
	prevSet := sigSet(prevSigs)
	nextSet := sigSet(nextSigs)
	for _, sig := range prevSigs {
		if _, ok := nextSet[sig]; !ok {
			n := prev.Nodes[sig]
			res.Events = append(res.Events, Event{
				RepoName:   repoName,
				FilePath:   prev.Path,
				Signature:  sig,
				NodeType:   n.Kind,
				Action:     ActionDeleted,
				BodyHash:   n.Hash,
				LOC:        n.LOC,
				OccurredAt: now,
			})
		}
	}

	// MODIFIED — same signature, hash changed.
	// ADDED — new signature.
	for _, sig := range nextSigs {
		newNode := next.Nodes[sig]
		if _, existed := prevSet[sig]; !existed {
			res.Events = append(res.Events, Event{
				RepoName:   repoName,
				FilePath:   next.Path,
				Signature:  sig,
				NodeType:   newNode.Kind,
				Action:     ActionAdded,
				BodyHash:   newNode.Hash,
				LOC:        newNode.LOC,
				OccurredAt: now,
			})
			continue
		}
		oldNode := prev.Nodes[sig]
		if oldNode.Hash != newNode.Hash {
			res.Events = append(res.Events, Event{
				RepoName:   repoName,
				FilePath:   next.Path,
				Signature:  sig,
				NodeType:   newNode.Kind,
				Action:     ActionModified,
				BodyHash:   newNode.Hash,
				LOC:        newNode.LOC,
				OccurredAt: now,
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
