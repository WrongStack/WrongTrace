package mcp

import (
	"strings"
	"testing"
	"time"

	"github.com/wrongstack/wrongtrace/internal/core"
	"github.com/wrongstack/wrongtrace/internal/ipc"
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

// TestCallTool_LockFileConflict_RedactsOwnerRunID pins the conflict branch of
// lock_file: when TryLockFile refuses because another owner holds the lock,
// the isError result's data carries the existing lock for diagnostics — but
// owner_run_id is the credential the unlock_file ownership check verifies,
// and the loser of a lock race must not receive it. The sibling surfaces
// (list_locks, get_file_health_score, check_guardrail) already redact the
// same field; the conflict payload was the one path that shipped it whole.
func TestCallTool_LockFileConflict_RedactsOwnerRunID(t *testing.T) {
	engine := newLockTestEngine(t)
	if _, err := engine.TryLockFile("src/secret.go", "refactor in progress", "agent-a", "agent-a-secret-run", time.Hour, false); err != nil {
		t.Fatalf("TryLockFile: %v", err)
	}

	resp := dispatch(engine, toolCallReq(1, "lock_file", `{"file_path":"src/secret.go","owner":"agent-b","reason":"mine now"}`))
	flag, text := isErrorResult(t, resp)
	if !flag || !strings.Contains(text, "agent-a") {
		t.Fatalf("conflict = isError:%v %q, want isError naming agent-a", flag, text)
	}

	wire := wireResult(t, resp)
	data, ok := wire["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("conflict data missing: %#v", wire["data"])
	}
	existing, ok := data["existing"].(map[string]interface{})
	if !ok {
		t.Fatalf("conflict existing missing: %#v", data)
	}
	if got := existing["owner_run_id"]; got != nil && got != "" {
		t.Fatalf("lock_file conflict leaked the ownership credential owner_run_id %q", got)
	}
	if existing["owner"] == "" || existing["path"] != "src/secret.go" {
		t.Errorf("diagnostic fields lost from conflict data: %#v", existing)
	}
	locked, info := engine.IsFileLocked("src/secret.go")
	if !locked || info.Owner != "agent-a" {
		t.Fatalf("holder's lock did not survive the conflict: locked=%v info=%+v", locked, info)
	}
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

// The lock-stealing credential leak: list_locks returned each lock
// owner_run_id — the credential the unlock_file ownership check verifies —
// letting any agent enumerate it and unlock a foreign lock. List output must
// carry no owner_run_id; the human-readable owner stays for diagnostics.
func TestCallTool_ListLocks_RedactsOwnerRunID(t *testing.T) {
	engine := newLockTestEngine(t)
	if _, err := engine.TryLockFile("src/secret.go", "refactor in progress", "agent-a", "agent-a-secret-run", time.Hour, false); err != nil {
		t.Fatalf("TryLockFile: %v", err)
	}

	resp := dispatch(engine, toolCallReq(7, "list_locks", `{}`))
	if resp.Error != nil {
		t.Fatalf("unexpected rpc error: %+v", resp.Error)
	}
	res, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected result map, got %#v", resp.Result)
	}
	locks, ok := res["data"].([]core.LockInfo)
	if !ok {
		t.Fatalf("expected []core.LockInfo in data, got %#v", res["data"])
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

// Same credential-leak class as list_locks: get_file_health_score returned
// the whole FileHealthReply including the lock owner_run_id — the credential
// the unlock_file ownership check verifies. Health output must carry no
// owner_run_id; the lock owner name stays.
func TestCallTool_GetFileHealthScore_RedactsOwnerRunID(t *testing.T) {
	sink := &fakeSink{health: ipc.FileHealthReply{
		FilePath:       "src/secret.go",
		HealthScore:    42,
		IsLocked:       true,
		LockReason:     "refactor in progress",
		LockOwner:      "agent-a",
		LockOwnerRunID: "agent-a-secret-run",
	}}

	resp := dispatch(sink, toolCallReq(8, "get_file_health_score", `{"file_path":"src/secret.go"}`))
	if resp.Error != nil {
		t.Fatalf("unexpected rpc error: %+v", resp.Error)
	}
	res, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected result map, got %#v", resp.Result)
	}
	h, ok := res["data"].(ipc.FileHealthReply)
	if !ok {
		t.Fatalf("expected FileHealthReply in data, got %#v", res["data"])
	}
	if h.LockOwnerRunID != "" {
		t.Fatalf("get_file_health_score leaked the ownership credential owner_run_id %q", h.LockOwnerRunID)
	}
	if !h.IsLocked || h.LockOwner != "agent-a" {
		t.Fatalf("redaction corrupted the health reply: %+v", h)
	}
}

// Pins check_guardrail's IsFileLocked branch: with the real engine holding a
// lock, the guardrail-block response reports the lock and carries no
// owner_run_id credential. The branch-rec text ends "is locked." without the
// reason parenthetical, so the assertion also proves which branch fired.
func TestCallTool_CheckGuardrail_LockedPath_RealEngine(t *testing.T) {
	engine := newLockTestEngine(t)
	if _, err := engine.TryLockFile("src/secret.go", "refactor in progress", "agent-a", "agent-a-secret-run", time.Hour, false); err != nil {
		t.Fatalf("TryLockFile: %v", err)
	}

	resp := dispatch(engine, toolCallReq(9, "check_guardrail", `{"file_path":"src/secret.go"}`))
	if resp.Error != nil {
		t.Fatalf("unexpected rpc error: %+v", resp.Error)
	}
	text := lockResultText(t, resp)
	res, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected result map, got %#v", resp.Result)
	}
	data, ok := res["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected data map, got %#v", res["data"])
	}
	if data["is_locked"] != true {
		t.Fatalf("expected is_locked=true, got %#v", data["is_locked"])
	}
	if _, leaked := data["owner_run_id"]; leaked {
		t.Fatal("check_guardrail leaked owner_run_id")
	}
	if _, leaked := data["lock_owner_run_id"]; leaked {
		t.Fatal("check_guardrail leaked lock_owner_run_id")
	}
	if data["lock_owner"] != "agent-a" || data["lock_reason"] != "refactor in progress" {
		t.Fatalf("unexpected lock metadata: %#v", data)
	}
	if !strings.Contains(text, "GUARDRAIL BLOCKED") {
		t.Fatalf("expected guardrail-block text, got %q", text)
	}
	if strings.Contains(text, "is locked (") {
		t.Fatalf("expected the IsFileLocked branch text, got the fallback text: %q", text)
	}
}

// healthFallbackSink hides the IsFileLocked capability so check_guardrail
// takes its FileHealth fallback branch. FileHealth mirrors the engine reply
// for the real engine lock — including a populated LockOwnerRunID, exactly
// what a store-backed engine would produce — so the test pins that the
// fallback response does not relay the credential even when the sink
// supplies it.
type healthFallbackSink struct {
	*core.Engine
}

func (s healthFallbackSink) IsFileLocked(string) (bool, core.LockInfo) {
	return false, core.LockInfo{}
}

func (s healthFallbackSink) FileHealth(path string) (ipc.FileHealthReply, error) {
	locked, info := s.Engine.IsFileLocked(path)
	if !locked {
		return ipc.FileHealthReply{FilePath: path, HealthScore: 100}, nil
	}
	exp := info.ExpiresAt
	return ipc.FileHealthReply{
		FilePath:       path,
		IsLocked:       true,
		LockReason:     info.Reason,
		LockOwner:      info.Owner,
		LockOwnerRunID: info.OwnerRunID,
		LockExpiresAt:  &exp,
	}, nil
}

// Pins check_guardrail's FileHealth fallback branch: a sink without the
// IsFileLocked capability still gets the guardrail-block response built from
// the real engine lock data, with no owner_run_id credential. The branch-rec
// text ends "is locked (...)" with the reason parenthetical, proving the
// fallback branch fired.
func TestCallTool_CheckGuardrail_LockedPath_HealthFallback(t *testing.T) {
	engine := newLockTestEngine(t)
	if _, err := engine.TryLockFile("src/secret.go", "refactor in progress", "agent-a", "agent-a-secret-run", time.Hour, false); err != nil {
		t.Fatalf("TryLockFile: %v", err)
	}

	resp := dispatch(healthFallbackSink{Engine: engine}, toolCallReq(10, "check_guardrail", `{"file_path":"src/secret.go"}`))
	if resp.Error != nil {
		t.Fatalf("unexpected rpc error: %+v", resp.Error)
	}
	text := lockResultText(t, resp)
	res, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected result map, got %#v", resp.Result)
	}
	data, ok := res["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected data map, got %#v", res["data"])
	}
	if data["is_locked"] != true {
		t.Fatalf("expected is_locked=true, got %#v", data["is_locked"])
	}
	if _, leaked := data["owner_run_id"]; leaked {
		t.Fatal("check_guardrail leaked owner_run_id")
	}
	if _, leaked := data["lock_owner_run_id"]; leaked {
		t.Fatal("check_guardrail leaked lock_owner_run_id")
	}
	if data["lock_owner"] != "agent-a" || data["lock_reason"] != "refactor in progress" {
		t.Fatalf("unexpected lock metadata: %#v", data)
	}
	if !strings.Contains(text, "is locked (") {
		t.Fatalf("expected the FileHealth fallback branch text, got: %q", text)
	}
}
