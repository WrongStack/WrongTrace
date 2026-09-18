package core

import (
	"errors"
	"testing"
	"time"
)

// Regression: Engine.UnlockFile must refuse to remove another agent's lock
// (ErrNotLockOwner) while still allowing the owner through. This is the
// engine-level enforcement behind the IPC/MCP unlock_file ownership check.
func TestUnlockFileRejectsForeignOwner(t *testing.T) {
	e := NewEngine(Config{RepoName: "unlock-owner-test"})
	const p = "src/important.go"

	if _, err := e.TryLockFile(p, "refactor in progress", "agent-a", "agent-a-run", time.Hour, false); err != nil {
		t.Fatalf("TryLockFile: %v", err)
	}

	if err := e.UnlockFile(p, "agent-b-run"); !errors.Is(err, ErrNotLockOwner) {
		t.Fatalf("foreign unlock err = %v, want ErrNotLockOwner", err)
	}

	locked, info := e.IsFileLocked(p)
	if !locked {
		t.Fatal("lock was removed by a foreign owner")
	}
	if info.Owner != "agent-a" {
		t.Fatalf("lock owner = %q, want agent-a", info.Owner)
	}

	if err := e.UnlockFile(p, "agent-a-run"); err != nil {
		t.Fatalf("owner unlock: %v", err)
	}
	if locked, _ := e.IsFileLocked(p); locked {
		t.Fatal("owner unlock left the lock in place")
	}
}

// Documented legacy edge (UnlockFile's ownership guard skips locks stored with
// an empty OwnerRunID): a lock that carries no verifiable owner may be cleared
// by any caller.
func TestUnlockFileAllowsForceUnlockOfOwnerlessLock(t *testing.T) {
	e := NewEngine(Config{RepoName: "unlock-owner-test"})
	const p = "src/legacy.go"

	if _, err := e.TryLockFile(p, "stale lock", "agent-x", "", time.Hour, false); err != nil {
		t.Fatalf("TryLockFile: %v", err)
	}
	if err := e.UnlockFile(p, "agent-y"); err != nil {
		t.Fatalf("force unlock of ownerless lock: %v", err)
	}
	if locked, _ := e.IsFileLocked(p); locked {
		t.Fatal("ownerless lock survived a force unlock")
	}
}
