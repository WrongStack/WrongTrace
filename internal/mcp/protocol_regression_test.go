package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// A >16 MB line used to make ServeStdio return an error, killing the whole
// MCP session. The line is now drained, answered with -32600, and the session
// keeps serving.
func TestServeStdio_OversizedLineDoesNotEndSession(t *testing.T) {
	out := runStdioSession(t, &fakeSink{}, []string{
		strings.Repeat("z", maxMCPLineBytes+1024),
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	})
	if len(out) != 2 {
		t.Fatalf("want 2 responses (-32600 + tools/list), got %d", len(out))
	}
	var tooLong, next jsonRPCResponse
	if err := json.Unmarshal([]byte(out[0]), &tooLong); err != nil {
		t.Fatalf("first response: %v", err)
	}
	if tooLong.Error == nil || tooLong.Error.Code != -32600 || tooLong.ID != nil {
		t.Fatalf("oversized line response = %+v, want -32600 with id null", tooLong)
	}
	if err := json.Unmarshal([]byte(out[1]), &next); err != nil {
		t.Fatalf("second response: %v", err)
	}
	if id, _ := next.ID.(float64); id != 2 || next.Error != nil {
		t.Fatalf("follow-up response = %+v, want id 2 success", next)
	}
}

func isErrorResult(t *testing.T, resp jsonRPCResponse) (bool, string) {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("tool outcome must be a result, got rpc error %+v", resp.Error)
	}
	res := wireResult(t, resp)
	flag, _ := res["isError"].(bool)
	return flag, fmt.Sprint(res["content"])
}

// lock_file silently replaced another owner's lock (the owner check existed
// only in the HTTP handler). The engine now refuses atomically and MCP reports
// the conflict as an isError tool result, leaving the holder's lock intact.
func TestCallTool_LockFile_RefusesToStealAnotherOwnersLock(t *testing.T) {
	engine := newLockTestEngine(t)
	if _, err := engine.TryLockFile("src/app.ts", "refactor", "alice", "run-a", time.Hour, false); err != nil {
		t.Fatalf("seed lock: %v", err)
	}

	resp := dispatch(engine, toolCallReq(1, "lock_file", `{"file_path":"src/app.ts","owner":"bob","reason":"mine now"}`))
	flag, text := isErrorResult(t, resp)
	if !flag || !strings.Contains(text, "alice") {
		t.Fatalf("conflict = isError:%v %q, want isError naming alice", flag, text)
	}
	if _, info := engine.IsFileLocked("src/app.ts"); info.Owner != "alice" || info.Reason != "refactor" {
		t.Fatalf("lock was stolen: %+v", info)
	}

	// Same owner refreshes; the holder is not blocked by its own lock.
	resp = dispatch(engine, toolCallReq(2, "lock_file", `{"file_path":"src/app.ts","owner":"alice","reason":"extended","ttl_minutes":90}`))
	if flag, text := isErrorResult(t, resp); flag {
		t.Fatalf("same-owner re-lock refused: %q", text)
	}
	assertLockLifetime(t, engine, "src/app.ts", 90*time.Minute)
}

func TestTryLockFile_ExpiredAndAnonymousLocksAreTakeable(t *testing.T) {
	engine := newLockTestEngine(t)

	if _, err := engine.TryLockFile("old.go", "", "alice", "", time.Millisecond, false); err != nil {
		t.Fatalf("seed: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	resp := dispatch(engine, toolCallReq(1, "lock_file", `{"file_path":"old.go","owner":"bob"}`))
	if flag, text := isErrorResult(t, resp); flag {
		t.Fatalf("expired lock not takeable: %q", text)
	}
	if _, info := engine.IsFileLocked("old.go"); info.Owner != "bob" {
		t.Fatalf("owner after takeover = %q, want bob", info.Owner)
	}

	// An owner-less (administrative) lock keeps its historical semantics.
	engine.LockFile("anon.go", "no owner")
	if flag, text := isErrorResult(t, dispatch(engine, toolCallReq(2, "lock_file", `{"file_path":"anon.go","owner":"bob"}`))); flag {
		t.Fatalf("anonymous lock not takeable: %q", text)
	}

	// force bypasses the check (the HTTP surface's explicit force flag).
	if _, err := engine.TryLockFile("anon.go", "", "carol", "", time.Hour, true); err != nil {
		t.Fatalf("forced lock refused: %v", err)
	}
	if _, err := engine.TryLockFile("anon.go", "", "dave", "", time.Hour, false); err == nil {
		t.Fatal("unforced lock over carol's lock succeeded")
	}
}
