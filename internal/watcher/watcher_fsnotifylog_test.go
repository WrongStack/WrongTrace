package watcher

// Regression coverage for the /debug/fsnotify capture buffer (bug-hunt
// round 10). FSNotifyLog's documented contract is "up to n most-recent
// captured fsnotify events, oldest first". The pre-fix implementation
// clamped n to the buffer CAPACITY instead of the captured count and walked
// backward from evHead (the next-write slot), so it returned unwritten
// zero-value entries before the buffer filled and delivered the oldest event
// last once the buffer wrapped. Events are injected deterministically via
// captureEvent — no kernel delivery — matching the TestDebounce_Synthetic*
// convention in this package.

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/fsnotify/fsnotify"
)

func newFSDebugWatcher(t *testing.T) *Watcher {
	t.Helper()
	w, err := New(Config{Dir: t.TempDir(), DebugFSEvents: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}

// An empty capture buffer must yield an empty log: unwritten slots are not
// events, and the SSE drain would otherwise stream thousands of zero-value
// entries right after daemon start.
func TestFSNotifyLog_EmptyBufferReturnsNoGarbage(t *testing.T) {
	w := newFSDebugWatcher(t)
	got := w.FSNotifyLog(10)
	if len(got) != 0 {
		t.Fatalf("FSNotifyLog(10) with 0 captured events returned %d entries; first: seq=%d path=%q", len(got), got[0].Seq, got[0].Path)
	}
}

func TestFSNotifyLog_DisabledReturnsNil(t *testing.T) {
	w, err := New(Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()
	if got := w.FSNotifyLog(10); got != nil {
		t.Fatalf("FSNotifyLog with DebugFSEvents disabled returned %v, want nil", got)
	}
}

// Before the buffer fills, FSNotifyLog returns exactly the captured events,
// oldest first; a window smaller than the fill returns the MOST RECENT n.
func TestFSNotifyLog_PartialFillReturnsExactlyCapturedOldestFirst(t *testing.T) {
	w := newFSDebugWatcher(t)
	for i := 0; i < 3; i++ {
		w.captureEvent(fsnotify.Event{Name: filepath.Join(w.cfg.Dir, fmt.Sprintf("f%d.txt", i)), Op: fsnotify.Create})
	}
	got := w.FSNotifyLog(10)
	if len(got) != 3 {
		t.Fatalf("3 captured events: FSNotifyLog(10) returned %d entries, want 3", len(got))
	}
	for i, ev := range got {
		if ev.Seq != uint64(i) {
			t.Fatalf("order broken at %d: seq=%d want %d", i, ev.Seq, i)
		}
	}
	got = w.FSNotifyLog(2)
	if len(got) != 2 || got[0].Seq != 1 || got[1].Seq != 2 {
		t.Fatalf("FSNotifyLog(2) returned [%d %d], want [1 2]", got[0].Seq, got[len(got)-1].Seq)
	}
}

// Once the buffer wraps, the full window must still be oldest-first with
// strictly increasing Seqs — the pre-fix backward walk started at evHead
// (the next-write slot) and appended the oldest event last.
func TestFSNotifyLog_FullBufferWrapKeepsStrictOrder(t *testing.T) {
	w := newFSDebugWatcher(t)
	// cap + 2 total captures: seqs 0 and 1 were evicted by the wrap, leaving
	// seqs [2 .. cap+1] live in a full buffer.
	for i := 0; i < len(w.evBuf)+2; i++ {
		w.captureEvent(fsnotify.Event{Name: filepath.Join(w.cfg.Dir, "wrap.txt"), Op: fsnotify.Write})
	}
	total := uint64(len(w.evBuf) + 2)
	got := w.FSNotifyLog(len(w.evBuf))
	if len(got) != len(w.evBuf) {
		t.Fatalf("full buffer returned %d entries, want %d", len(got), len(w.evBuf))
	}
	oldest, newest := total-uint64(len(w.evBuf)), total-1
	if got[0].Seq != oldest || got[len(got)-1].Seq != newest {
		t.Fatalf("window = [seq %d..%d], want [%d..%d]", got[0].Seq, got[len(got)-1].Seq, oldest, newest)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Seq != got[i-1].Seq+1 {
			t.Fatalf("seqs not strictly increasing at %d: %d follows %d", i, got[i].Seq, got[i-1].Seq)
		}
	}
}
