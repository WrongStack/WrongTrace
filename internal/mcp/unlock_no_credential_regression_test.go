package mcp

import "testing"

// Omitting owner_run_id must not turn unlock_file into a force unlock: an
// agent could otherwise release any other run's live lock just by leaving its
// credential out, while presenting a wrong one is refused.
func TestCallTool_UnlockWithoutCredentialKeepsOwnedLock(t *testing.T) {
	engine := newLockTestEngine(t)

	if resp := dispatch(engine, toolCallReq(1, "lock_file", `{"file_path":"src/a.go","owner":"agent-a","owner_run_id":"run-a","ttl_minutes":60}`)); resp.Error != nil {
		t.Fatalf("lock_file failed: %+v", resp.Error)
	}
	resp := dispatch(engine, toolCallReq(2, "unlock_file", `{"file_path":"src/a.go"}`))
	if !isToolError(resp) {
		t.Errorf("unlock_file without owner_run_id succeeded on a lock owned by run-a: %+v", resp.Result)
	}
	if locked, _ := engine.IsFileLocked("src/a.go"); !locked {
		t.Fatal("run-a's lock was released by a caller without its credential")
	}

	// Control: the owner's own credential still releases it.
	if resp := dispatch(engine, toolCallReq(3, "unlock_file", `{"file_path":"src/a.go","owner_run_id":"run-a"}`)); isToolError(resp) {
		t.Fatalf("owner unlock failed: %+v", resp)
	}
	if locked, _ := engine.IsFileLocked("src/a.go"); locked {
		t.Fatal("owner unlock left the lock in place")
	}
}

func isToolError(resp jsonRPCResponse) bool {
	if resp.Error != nil {
		return true
	}
	res, ok := resp.Result.(map[string]interface{})
	return ok && res["isError"] == true
}
