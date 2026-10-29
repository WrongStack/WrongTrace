package core

import (
	"reflect"
	"testing"
	"time"
)

func TestPrimeDirectoryClosedEngineRejectsDelayedStart(t *testing.T) {
	for _, delayed := range []bool{false, true} {
		name := "after-close"
		if delayed {
			name = "entry-delayed-until-after-close"
		}
		t.Run(name, func(t *testing.T) {
			e, _, parser := newAtlasTestEngine(t)
			t.Cleanup(e.Close)
			controlRoot := t.TempDir()
			control := writeFixture(t, controlRoot, "control.go", "package demo\nfunc Control() {}\n")
			e.PrimeDirectory(controlRoot)
			if _, ok := parser.Snapshot(control); !ok {
				t.Fatal("control: open engine did not index the file")
			}
			t.Log("CONTROL EXPECTED: open engine indexes file | ACTUAL: true")
			before := e.IndexStatus()
			subjectRoot := t.TempDir()
			subject := writeFixture(t, subjectRoot, "subject.go", "package demo\nfunc Subject() {}\n")
			done := make(chan struct{})
			if delayed {
				// Block the entry before it can register a cancellation context.
				// Closing the owner happens before that entry is allowed to resume.
				e.rootMu.Lock()
				entered := make(chan struct{})
				go func() {
					close(entered)
					e.PrimeDirectory(subjectRoot)
					close(done)
				}()
				<-entered
				e.Close()
				e.rootMu.Unlock()
			} else {
				e.Close()
				e.PrimeDirectory(subjectRoot)
				close(done)
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("delayed prime did not return")
			}
			_, indexed := parser.Snapshot(subject)
			t.Logf("EXPECTED: closed engine does not index delayed file | ACTUAL: indexed=%v", indexed)
			if indexed || !reflect.DeepEqual(e.IndexStatus(), before) {
				t.Fatal("closed engine restarted indexing and changed snapshots/progress")
			}
			e.Close() // closing repeatedly must retain the admission barrier
			e.PrimeDirectory(subjectRoot)
			if _, ok := parser.Snapshot(subject); ok {
				t.Fatal("repeat close allowed indexing")
			}
		})
	}
}

func TestPrimeDirectoryCancellationStillAllowsLaterIndex(t *testing.T) {
	e, _, parser := newAtlasTestEngine(t)
	t.Cleanup(e.Close)
	root := t.TempDir()
	file := writeFixture(t, root, "retry.go", "package demo\nfunc Retry() {}\n")
	e.cancelPrime() // project switches cancel work without closing the owner
	e.PrimeDirectory(root)
	if _, ok := parser.Snapshot(file); !ok {
		t.Fatal("cancellation must allow later indexing")
	}
	e.PrimeDirectory("")
	if e.IndexStatus().IsIndexing {
		t.Fatal("empty indexing request should be a no-op")
	}
}
