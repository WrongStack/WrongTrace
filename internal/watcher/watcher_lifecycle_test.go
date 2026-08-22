package watcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ---------------------------------------------------------------
// Run-loop branches
// ---------------------------------------------------------------

// TestRun_NewDirectoryIsWatchedRecursively pins the dynamic-recursion
// behavior: a directory tree created AFTER Run started is picked up via the
// Create -> addRecursive path, so a file written deep inside it delivers.
func TestRun_NewDirectoryIsWatchedRecursively(t *testing.T) {
	root := t.TempDir()
	_, h := startWatcher(t, root, 60*time.Millisecond, nil)

	b1 := filepath.Join(root, "brand")
	_ = os.Mkdir(b1, 0o755)
	time.Sleep(300 * time.Millisecond)

	nested := filepath.Join(b1, "new")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Let the Create events reach the loop so addRecursive registers both levels
	time.Sleep(500 * time.Millisecond)

	path := filepath.Join(nested, "deep.go")
	if err := os.WriteFile(path, []byte("package new\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return h.countFor("deep.go") >= 1 },
		"file inside newly created dir was never watched (Create->addRecursive broken)")
}

// TestRun_CloseStopsEventLoop pins the shutdown contract: closing the
// underlying fsnotify watcher closes the Events channel, and Run returns.
func TestRun_CloseStopsEventLoop(t *testing.T) {
	dir := t.TempDir()
	h := &recordingHandler{}
	w, err := New(Config{Dir: dir, Engine: h, Debounce: 30 * time.Millisecond})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(returned)
	}()

	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case <-returned:
		// Run observed the closed Events channel and returned.
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after Close")
	}
	cancel()
}

// TestRun_IrrelevantOpIsIgnored injects a CHMOD (the canonical irrelevant
// op) and asserts no debounce callback is ever scheduled.
func TestRun_IrrelevantOpIsIgnored(t *testing.T) {
	dir := t.TempDir()
	h := &recordingHandler{}
	w, err := New(Config{Dir: dir, Engine: h, Debounce: 30 * time.Millisecond})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	time.Sleep(100 * time.Millisecond) // let the loop start

	w.fs.Events <- fsnotify.Event{Name: filepath.Join(dir, "x.go"), Op: fsnotify.Chmod}

	waitQuiet(t, 300*time.Millisecond, func() bool { return h.count() > 0 },
		"CHMOD-only event must never schedule HandleFileChange")
}

// TestRun_ErrorsChannelIsLoggedAndSurvived injects a synthetic watcher
// error; the loop must log it and keep serving subsequent events.
func TestRun_ErrorsChannelIsLoggedAndSurvived(t *testing.T) {
	dir := t.TempDir()
	h := &recordingHandler{}
	w, err := New(Config{Dir: dir, Engine: h, Debounce: 40 * time.Millisecond})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	w.fs.Errors <- fmt.Errorf("synthetic fsnotify error")

	// Loop survives: a real write still delivers.
	path := filepath.Join(dir, "after-error.go")
	if err := os.WriteFile(path, []byte("package after\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitFor(t, 3*time.Second, func() bool { return h.countFor("after-error.go") >= 1 },
		"Run loop died after an fsnotify error")
}

// TestRun_ErrorsChannelClosedStopsLoop pins the second Errors-channel
// branch: a closed Errors channel terminates Run.
func TestRun_ErrorsChannelClosedStopsLoop(t *testing.T) {
	dir := t.TempDir()
	h := &recordingHandler{}
	w, err := New(Config{Dir: dir, Engine: h, Debounce: 30 * time.Millisecond})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(returned)
	}()
	time.Sleep(100 * time.Millisecond)

	w.fs.Errors <- fmt.Errorf("ignored") // drained first so close() is clean
	close(w.fs.Errors)

	select {
	case <-returned:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after Errors channel closed")
	}
}
