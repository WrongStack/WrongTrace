package mcp

import (
	"strings"
	"testing"
	"time"

	"github.com/wrongstack/wrongtrace/internal/core"
)

// Round-24 regression: callTool's lock capability assertions used result-less
// method signatures, which *core.Engine — the production sink, passed straight
// to ServeStdio by cmd/wrongtrace — can never satisfy, because its Lock
// methods return core.LockInfo and Go interface satisfaction requires exact
// result types. lock_file therefore reported "locked successfully" while
// taking no lock at all. These tests pin the real-engine lock behavior end to
// end through dispatch.

func newLockTestEngine(t *testing.T) *core.Engine {
	t.Helper()
	t.Setenv("WRONGTRACE_HOME", t.TempDir())
	return core.NewEngine(core.Config{})
}

func lockResultText(t *testing.T, resp jsonRPCResponse) string {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("unexpected rpc error: %+v", resp.Error)
	}
	res, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected result map, got %#v", resp.Result)
	}
	content, ok := res["content"].([]map[string]interface{})
	if !ok || len(content) == 0 {
		t.Fatalf("expected content array, got %#v", res["content"])
	}
	text, _ := content[0]["text"].(string)
	return text
}

func assertLockLifetime(t *testing.T, engine *core.Engine, path string, want time.Duration) {
	t.Helper()
	locked, info := engine.IsFileLocked(path)
	if !locked {
		t.Fatalf("expected an active lock on %s, found none", path)
	}
	if d := info.ExpiresAt.Sub(info.LockedAt); d < want-time.Minute || d > want+time.Minute {
		t.Fatalf("lock lifetime = %v, want ~%v", d, want)
	}
}

func TestCallTool_LockFile_RealEngineTakesLock(t *testing.T) {
	engine := newLockTestEngine(t)

	req := toolCallReq(1, "lock_file", `{"file_path":"src/app.ts","reason":"audit","owner":"probe","ttl_minutes":60}`)
	resp := dispatch(engine, req)
	if text := lockResultText(t, resp); !strings.Contains(text, "locked successfully") {
		t.Fatalf("lock_file did not report success: %q", text)
	}

	// The success response must correspond to a real engine lock with the
	// requested lifetime — the round-24 bug reported success with zero locks.
	locked, _ := engine.IsFileLocked("src/app.ts")
	if !locked {
		t.Fatal("lock_file reported success but no lock exists in the engine")
	}
	assertLockLifetime(t, engine, "src/app.ts", 60*time.Minute)
	if got := len(engine.ListLocks()); got != 1 {
		t.Fatalf("engine holds %d locks, want 1", got)
	}
}

func TestCallTool_LockFile_RejectsTTLOverflow(t *testing.T) {
	engine := newLockTestEngine(t)

	// time.Duration is int64 nanoseconds: 153722868 minutes is the first
	// value whose nanosecond product overflows and wraps negative; the
	// engine would silently degrade it to a 15-minute default. It must be
	// rejected instead of substituted.
	for name, args := range map[string]string{
		"ttl_minutes": `{"file_path":"a.go","ttl_minutes":153722868}`,
		"ttl_seconds": `{"file_path":"b.go","ttl_seconds":10000000000000}`,
	} {
		resp := dispatch(engine, toolCallReq(2, "lock_file", args))
		if resp.Error == nil || resp.Error.Code != -32602 {
			t.Fatalf("%s: expected -32602 for overflowing TTL, got %+v", name, resp.Error)
		}
	}
	if len(engine.ListLocks()) != 0 {
		t.Fatalf("rejected TTLs must not create locks, found %d", len(engine.ListLocks()))
	}
}

func TestCallTool_LockFile_SecondsAndDefaultTTL(t *testing.T) {
	engine := newLockTestEngine(t)

	resp := dispatch(engine, toolCallReq(3, "lock_file", `{"file_path":"a.go","ttl_seconds":3600}`))
	lockResultText(t, resp)
	assertLockLifetime(t, engine, "a.go", time.Hour)

	// Missing/nonpositive TTL keeps the documented 15-minute default.
	engine2 := newLockTestEngine(t)
	resp = dispatch(engine2, toolCallReq(4, "lock_file", `{"file_path":"b.go"}`))
	lockResultText(t, resp)
	assertLockLifetime(t, engine2, "b.go", 15*time.Minute)
}

func TestCallTool_UnlockFile_RealEngine(t *testing.T) {
	engine := newLockTestEngine(t)

	if resp := dispatch(engine, toolCallReq(5, "lock_file", `{"file_path":"c.go","ttl_minutes":60}`)); resp.Error != nil {
		t.Fatalf("lock_file failed: %+v", resp.Error)
	}
	if resp := dispatch(engine, toolCallReq(6, "unlock_file", `{"file_path":"c.go"}`)); resp.Error != nil {
		t.Fatalf("unlock_file failed: %+v", resp.Error)
	}
	if locked, _ := engine.IsFileLocked("c.go"); locked {
		t.Fatal("unlock_file reported success but the lock still exists")
	}
	if got := len(engine.ListLocks()); got != 0 {
		t.Fatalf("engine holds %d locks after unlock, want 0", got)
	}
}
