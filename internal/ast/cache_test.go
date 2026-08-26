package ast

import (
	"strings"
	"testing"
)

func snapshotWithSource(path, src string) *FileSnapshot {
	return &FileSnapshot{
		Path:       path,
		Nodes:      map[string]Node{},
		Hash:       hashBytes([]byte(src)),
		RawContent: src,
	}
}

// TestSourceRoundTripsThroughCompression is the correctness floor for the
// packed cache: whatever Parse produced must come back byte-identical, because
// Diff slices node bodies straight out of it.
func TestSourceRoundTripsThroughCompression(t *testing.T) {
	src := strings.Repeat("func handler(w http.ResponseWriter, r *http.Request) {}\n", 200)
	snap := snapshotWithSource("a.go", src)
	snap.pack()

	if snap.RawContent != "" {
		t.Fatal("pack left the plain source in place; nothing was saved")
	}
	if got := snap.Source(); got != src {
		t.Fatalf("Source() round-trip mismatch: got %d bytes, want %d", len(got), len(src))
	}
	if snap.retainedBytes() >= int64(len(src)) {
		t.Fatalf("compressed size %d did not beat raw size %d", snap.retainedBytes(), len(src))
	}
}

// TestIncompressibleSourceStaysPlain covers the fallback: random-looking data
// must not be stored larger than it started.
func TestIncompressibleSourceStaysPlain(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 512; i++ {
		b.WriteByte(byte(i*7 + i*i*13))
	}
	src := b.String()
	snap := snapshotWithSource("blob.go", src)
	snap.pack()
	if got := snap.Source(); got != src {
		t.Fatalf("incompressible source lost: got %d bytes, want %d", len(got), len(src))
	}
}

// TestSetSnapshotEvictsColdestSourceFirst proves the byte budget is enforced
// and that eviction is graceful: the node map -- what signature-level diffing
// needs -- must survive even when the source is dropped.
func TestSetSnapshotEvictsColdestSourceFirst(t *testing.T) {
	body := strings.Repeat("x := compute(alpha, beta, gamma)\n", 400)
	paths := []string{"one.go", "two.go", "three.go", "four.go", "five.go"}

	// Calibrate the budget from the real compressed size so the test asserts
	// eviction ORDER rather than a guess about the compression ratio. Room for
	// roughly two entries: the newest must survive, the coldest must not.
	probe := snapshotWithSource(paths[0], paths[0]+"\n"+body)
	probe.pack()
	restore := sourceBudgetBytes
	sourceBudgetBytes = probe.retainedBytes()*2 + 8
	defer func() { sourceBudgetBytes = restore }()

	e, err := NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	for _, p := range paths {
		snap := snapshotWithSource(p, p+"\n"+body)
		snap.Nodes["func:"+p+"::run()"] = Node{Signature: "func:" + p + "::run()", Hash: "h-" + p}
		e.SetSnapshot(snap)
	}

	if got := e.CachedSourceBytes(); got > sourceBudgetBytes {
		t.Fatalf("retained %d bytes, over the %d budget", got, sourceBudgetBytes)
	}

	// The oldest write is the first to lose its source; the newest keeps it.
	oldest, ok := e.Snapshot(paths[0])
	if !ok {
		t.Fatal("evicting source must not drop the snapshot itself")
	}
	if oldest.Source() != "" {
		t.Fatal("coldest snapshot kept its source despite the budget")
	}
	if len(oldest.Nodes) != 1 {
		t.Fatal("eviction destroyed the node map; signature diffing would break")
	}

	newest, ok := e.Snapshot(paths[len(paths)-1])
	if !ok {
		t.Fatal("newest snapshot missing")
	}
	if newest.Source() == "" {
		t.Fatal("newest snapshot was evicted before colder ones")
	}
}

// TestSetSnapshotRefundsReplacedEntry guards the accounting: a file edited over
// and over must not inflate the retained-byte total with corpses.
func TestSetSnapshotRefundsReplacedEntry(t *testing.T) {
	e, err := NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	body := strings.Repeat("call(a, b)\n", 300)
	for i := 0; i < 20; i++ {
		snap := snapshotWithSource("hot.go", body)
		e.SetSnapshot(snap)
	}
	single := snapshotWithSource("probe.go", body)
	single.pack()

	if got, want := e.CachedSourceBytes(), single.retainedBytes()*2; got > want {
		t.Fatalf("20 rewrites of one file retained %d bytes; a single copy is %d", got, single.retainedBytes())
	}
}

// TestForgetReleasesBudget keeps deletions from leaking accounting.
func TestForgetReleasesBudget(t *testing.T) {
	e, err := NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	e.SetSnapshot(snapshotWithSource("gone.go", strings.Repeat("y := 1\n", 500)))
	if e.CachedSourceBytes() == 0 {
		t.Fatal("nothing was retained to begin with")
	}
	e.Forget("gone.go")
	if got := e.CachedSourceBytes(); got != 0 {
		t.Fatalf("Forget left %d bytes charged to the budget", got)
	}
}
