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

// Regression (r-unlock-nest-alias): UnlockFile's ownership guard must resolve
// locks the same way its delete loop does — nesting-aware. The guard used to
// look up only the exact map key, so a caller spelling a locked file through a
// nesting alias ("main.go" for a lock held as "src/main.go", or the reverse)
// bypassed the ErrNotLockOwner check while the nesting-aware delete still
// removed the foreign lock and UnlockFile returned nil.
func TestUnlockFileForeignOwnerViaAliasSpelling(t *testing.T) {
	e := NewEngine(Config{RepoName: "unlock-alias-test"})
	const held = "src/main.go"

	if _, err := e.TryLockFile(held, "refactor in progress", "agent-a", "agent-a-run", time.Hour, false); err != nil {
		t.Fatalf("TryLockFile: %v", err)
	}

	// Alias spellings of the SAME file must hit the same ownership guard as
	// the exact spelling (pinned by TestUnlockFileRejectsForeignOwner).
	for _, alias := range []string{"main.go", "src/./main.go", `src\main.go`} {
		if err := e.UnlockFile(alias, "agent-b-run"); !errors.Is(err, ErrNotLockOwner) {
			t.Fatalf("aliased foreign unlock %q err = %v, want ErrNotLockOwner", alias, err)
		}
		locked, info := e.IsFileLocked(held)
		if !locked {
			t.Fatalf("aliased foreign unlock %q removed agent-a's lock", alias)
		}
		if info.Owner != "agent-a" {
			t.Fatalf("lock owner = %q after aliased attempt, want agent-a", info.Owner)
		}
	}

	// The stored lock's owner must still get through by an alias spelling,
	// and the delete must stay nesting-scoped: a same-directory file whose
	// name merely extends the locked name is a different file.
	if err := e.UnlockFile("main.go", "agent-a-run"); err != nil {
		t.Fatalf("owner unlock via alias: %v", err)
	}
	if locked, _ := e.IsFileLocked(held); locked {
		t.Fatal("owner unlock via alias left the lock in place")
	}

	// Reverse direction: lock stored under the short spelling, caller names
	// the nested spelling.
	if _, err := e.TryLockFile("main.go", "held", "agent-a", "agent-a-run", time.Hour, false); err != nil {
		t.Fatalf("TryLockFile reverse: %v", err)
	}
	if err := e.UnlockFile("src/main.go", "agent-b-run"); !errors.Is(err, ErrNotLockOwner) {
		t.Fatalf("reverse aliased foreign unlock err = %v, want ErrNotLockOwner", err)
	}
	if locked, _ := e.IsFileLocked("main.go"); !locked {
		t.Fatal("reverse aliased foreign unlock removed agent-a's lock")
	}
	if err := e.UnlockFile("src/main.go", "agent-a-run"); err != nil {
		t.Fatalf("reverse owner unlock: %v", err)
	}

	// Boundary: an EXPIRED foreign lock stays clearable through an alias,
	// matching the exact-spelling behavior (expired locks are free to clear).
	if _, err := e.TryLockFile("src/expired.go", "held", "agent-a", "agent-a-run", 10*time.Millisecond, false); err != nil {
		t.Fatalf("TryLockFile expired setup: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	if err := e.UnlockFile("expired.go", "agent-b-run"); err != nil {
		t.Fatalf("aliased unlock of expired lock: %v", err)
	}
	if locked, _ := e.IsFileLocked("src/expired.go"); locked {
		t.Fatal("aliased unlock failed to clear an expired foreign lock")
	}
}
