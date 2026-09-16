package ipc

import (
	"errors"
	"testing"
)

// Round-60 contract: dispatch must forward the caller-supplied owner_run_id to
// Engine.UnlockFile so the engine can enforce per-agent lock ownership
// (ErrNotLockOwner for a foreign owner).
func TestDispatchUnlockFileForwardsOwnerRunID(t *testing.T) {
	sink := &fakeSink{}
	srv := &Server{cfg: Config{Engine: sink}}

	resp := srv.dispatch(&Request{
		Method: "unlock_file",
		Params: map[string]interface{}{
			"file_path":    "src/important.go",
			"owner_run_id": "run-bbb",
		},
	})
	if resp.Error != nil {
		t.Fatalf("dispatch returned error: %v", resp.Error)
	}
	if sink.unlockCalls != 1 {
		t.Fatalf("UnlockFile calls = %d, want 1", sink.unlockCalls)
	}
	if sink.unlockPath != "src/important.go" {
		t.Errorf("UnlockFile path = %q, want %q", sink.unlockPath, "src/important.go")
	}
	if sink.unlockOwnerRunID != "run-bbb" {
		t.Errorf("UnlockFile ownerRunID = %q, want %q", sink.unlockOwnerRunID, "run-bbb")
	}
}

// Reproduction: when the engine refuses an unlock (ErrNotLockOwner for a
// foreign owner), dispatch must surface the refusal as an RPC error instead of
// reporting "status": "unlocked". The MCP layer already propagates this error
// via toolError (internal/mcp/server.go); the IPC dispatch discarded it and
// answered a lock-steal attempt with success.
func TestDispatchUnlockFileSurfacesEngineError(t *testing.T) {
	sink := &fakeSink{unlockErr: errors.New("unlock denied: caller does not own the lock")}
	srv := &Server{cfg: Config{Engine: sink}}

	resp := srv.dispatch(&Request{
		Method: "guardrail/unlock",
		Params: map[string]interface{}{
			"path":         "src/important.go",
			"owner_run_id": "run-bbb",
		},
	})
	if resp.Error == nil {
		t.Fatalf("dispatch reported success (%v) despite the engine refusing the unlock", resp.Result)
	}
	if resp.Error.Message == "" {
		t.Fatal("RPC error carries no message")
	}
	if sink.unlockCalls != 1 {
		t.Fatalf("UnlockFile calls = %d, want 1", sink.unlockCalls)
	}
}

// The lock-stealing credential leak: list_locks serialized each lock
// owner_run_id — the credential the unlock_file ownership check verifies —
// letting any agent enumerate it and unlock a foreign lock. List output must
// carry no owner_run_id; the human-readable owner stays for diagnostics.
func TestDispatchListLocksRedactsOwnerRunID(t *testing.T) {
	sink := &fakeSink{listLocks: []LockInfo{{
		Path:       "src/secret.go",
		Reason:     "refactor in progress",
		Owner:      "agent-a",
		OwnerRunID: "agent-a-secret-run",
	}}}
	srv := &Server{cfg: Config{Engine: sink}}

	resp := srv.dispatch(&Request{Method: "list_locks"})
	if resp.Error != nil {
		t.Fatalf("dispatch returned error: %v", resp.Error)
	}
	res, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected result map, got %#v", resp.Result)
	}
	locks, ok := res["locks"].([]LockInfo)
	if !ok {
		t.Fatalf("expected []LockInfo in result, got %#v", res["locks"])
	}
	if len(locks) != 1 {
		t.Fatalf("expected 1 lock in list, got %d", len(locks))
	}
	if locks[0].OwnerRunID != "" {
		t.Fatalf("list_locks leaked the ownership credential owner_run_id %q", locks[0].OwnerRunID)
	}
	if locks[0].Owner != "agent-a" || locks[0].Path != "src/secret.go" {
		t.Fatalf("redaction corrupted the lock entry: %+v", locks[0])
	}
}

// Same credential-leak class as list_locks: file_health serialized the lock
// owner_run_id — the credential the unlock_file ownership check verifies —
// letting any agent enumerate it and unlock a foreign lock. file_health
// output must carry no owner_run_id; the lock owner name stays.
func TestDispatchFileHealthRedactsOwnerRunID(t *testing.T) {
	sink := &fakeSink{healthOut: FileHealthReply{
		FilePath:       "src/secret.go",
		HealthScore:    42,
		IsLocked:       true,
		LockReason:     "refactor in progress",
		LockOwner:      "agent-a",
		LockOwnerRunID: "agent-a-secret-run",
	}}
	srv := &Server{cfg: Config{Engine: sink}}

	resp := srv.dispatch(&Request{
		Method: "file_health",
		Params: map[string]interface{}{"file_path": "src/secret.go"},
	})
	if resp.Error != nil {
		t.Fatalf("dispatch returned error: %v", resp.Error)
	}
	h, ok := resp.Result.(FileHealthReply)
	if !ok {
		t.Fatalf("expected FileHealthReply result, got %#v", resp.Result)
	}
	if h.LockOwnerRunID != "" {
		t.Fatalf("file_health leaked the ownership credential owner_run_id %q", h.LockOwnerRunID)
	}
	if !h.IsLocked || h.LockOwner != "agent-a" || h.LockReason != "refactor in progress" {
		t.Fatalf("redaction corrupted the health reply: %+v", h)
	}
}
