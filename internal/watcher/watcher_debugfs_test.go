package watcher

// Regression coverage for the /debug/fsnotify SSE endpoint (bug-hunt round
// 11). The pre-fix handler never seeded lastSeq from the initial drain, so
// the first 200 ms poll re-emitted the entire buffer the client had just
// received; and because lastSeq defaulted to 0, a client that connected on
// an EMPTY buffer never received the first captured event (Seq 0) at all.
// The handler now records the newest seq delivered by the drain and uses a
// seenAny flag so an empty drain does not pre-mark Seq 0 as seen. These
// tests drive the real handler over HTTP (httptest) with deterministic
// captureEvent injection — no kernel event delivery.

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// startSSE connects to the watcher's debug SSE endpoint and returns a channel
// receiving the seq of every delivered event line, plus a cancel func. The
// handler exits when the request context is cancelled.
func startSSE(t *testing.T, w *Watcher) (seqs <-chan uint64, cancel func()) {
	t.Helper()
	srv := httptest.NewServer(w.Handler())
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		cancel()
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("GET debug SSE endpoint: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("GET status = %d, want 200", resp.StatusCode)
	}
	ch := make(chan uint64, 64)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			const p = `data: {"seq":`
			line := sc.Text()
			if !strings.HasPrefix(line, p) {
				continue
			}
			num := line[len(p):]
			if i := strings.IndexByte(num, ','); i >= 0 {
				num = num[:i]
			}
			seq, err := strconv.ParseUint(num, 10, 64)
			if err != nil {
				continue
			}
			ch <- seq
		}
	}()
	return ch, cancel
}

// nextSeq reads the next delivered seq, or reports false if none arrives
// within the window.
func nextSeq(t *testing.T, ch <-chan uint64, within time.Duration) (uint64, bool) {
	t.Helper()
	select {
	case seq, ok := <-ch:
		return seq, ok
	case <-time.After(within):
		return 0, false
	}
}

// A client must receive the drain exactly once, then only genuinely new
// events: the first delivery after the drain must be the newly captured
// event, never a replay of the drained window.
func TestDebugFSNotifyHandler_NoDuplicateAfterDrain(t *testing.T) {
	dir := t.TempDir()
	w, err := New(Config{Dir: dir, DebugFSEvents: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	for i := 0; i < 3; i++ {
		w.captureEvent(fsnotify.Event{Name: filepath.Join(dir, fmt.Sprintf("f%d.txt", i)), Op: fsnotify.Create})
	}
	seqs, cancel := startSSE(t, w)
	defer cancel()

	for want := uint64(0); want < 3; want++ {
		got, ok := nextSeq(t, seqs, 2*time.Second)
		if !ok {
			t.Fatalf("drain ended early; wanted seq %d", want)
		}
		if got != want {
			t.Fatalf("drain delivered seq %d, want %d", got, want)
		}
	}

	w.captureEvent(fsnotify.Event{Name: filepath.Join(dir, "late.txt"), Op: fsnotify.Write})
	got, ok := nextSeq(t, seqs, 2*time.Second)
	if !ok {
		t.Fatalf("late-captured event seq 3 not delivered within 2s")
	}
	if got != 3 {
		t.Fatalf("first delivery after the drain was seq %d, want 3 — the first poll re-emitted the already-drained buffer", got)
	}

	// The buffer holds no unseen events, so the poll loop must stay silent.
	if got, ok := nextSeq(t, seqs, 400*time.Millisecond); ok {
		t.Fatalf("unexpected extra delivery seq %d after the poll caught up", got)
	}
}

// A client that connects before any event exists must still receive the
// first captured event (Seq 0): an empty drain must not pre-mark it seen.
func TestDebugFSNotifyHandler_EmptyBufferDeliversFirstEvent(t *testing.T) {
	dir := t.TempDir()
	w, err := New(Config{Dir: dir, DebugFSEvents: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	seqs, cancel := startSSE(t, w)
	defer cancel()

	w.captureEvent(fsnotify.Event{Name: filepath.Join(dir, "first.txt"), Op: fsnotify.Create})
	got, ok := nextSeq(t, seqs, 2*time.Second)
	if !ok {
		t.Fatalf("first captured event (seq 0) never delivered to a client connected on an empty buffer")
	}
	if got != 0 {
		t.Fatalf("expected seq 0, got %d", got)
	}
}

// The endpoint rejects watchers started without DebugFSEvents.
func TestDebugFSNotifyHandler_DisabledRejected(t *testing.T) {
	w, err := New(Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	rr := httptest.NewRecorder()
	debugFSNotifyHandler(w).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("disabled debug endpoint returned status %d, want 400", rr.Code)
	}
}
