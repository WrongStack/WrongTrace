package ipc

// Round: the lock ownership credential must not reach an agent through the
// guardrail surfaces.
//
// owner_run_id is the credential Engine.UnlockFile verifies
// (internal/core/guardrails.go: ErrNotLockOwner unless the caller presents the
// lock's stored OwnerRunID). Earlier rounds redacted it from IPC list_locks,
// IPC file_health and the MCP surfaces on the stated ground that "broadcasting
// it would let any agent steal locks", but the check_guardrail branch was never
// given the same treatment: unlike its siblings it serialized the whole
// GuardrailResult struct, whose lock_owner_run_id field the engine fills from
// the live lock map.
//
// A caller that merely asks "is it safe to edit this file?" was handed the
// authorization to unlock the file it was just refused.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// guardrailCredSink answers the guardrail surfaces the way the engine answers
// for a file held by ANOTHER agent: holder name plus holder credential. It
// overrides only CheckGuardrail and inherits the rest of EngineSink.
type guardrailCredSink struct {
	*fakeSink
}

const guardrailHolderCred = "holder-secret-run"

func (s *guardrailCredSink) CheckGuardrail(p string) (GuardrailResult, error) {
	exp := time.Now().UTC().Add(15 * time.Minute)
	return GuardrailResult{
		Allowed:        false,
		IsLocked:       true,
		LockReason:     "refactor in progress",
		LockOwner:      "agent-a",
		LockOwnerRunID: guardrailHolderCred,
		LockExpiresAt:  &exp,
		Recommendation: "BLOCKED: File is locked against automated agent changes.",
	}, nil
}

// TestDispatchCheckGuardrailRedactsOwnerRunID is the regression guard.
func TestDispatchCheckGuardrailRedactsOwnerRunID(t *testing.T) {
	srv := NewServer(Config{Engine: &guardrailCredSink{fakeSink: &fakeSink{}}})
	resp := srv.dispatch(&Request{
		JSONRPC: "2.0",
		Method:  "check_guardrail",
		Params:  params(t, `{"file_path":"src/a.go"}`),
		ID:      float64(1),
	})
	if resp.Error != nil {
		t.Fatalf("dispatch error: %+v", resp.Error)
	}

	// Assert on the serialized form: the contract is what the wire carries.
	b, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	// Precondition: this really was the locked verdict, so a missing key below
	// means redaction and not an empty reply.
	if m["is_locked"] != true {
		t.Fatalf("result is not the locked verdict: %s", b)
	}
	if m["lock_owner"] != "agent-a" {
		t.Fatalf("result lost the human-readable holder name (must stay for diagnostics): %s", b)
	}
	if m["lock_reason"] != "refactor in progress" {
		t.Fatalf("result lost the lock reason (must stay for diagnostics): %s", b)
	}

	if v, present := m["lock_owner_run_id"]; present && v != "" {
		t.Fatalf("check_guardrail leaked the ownership credential %q; a guardrail READ must not hand out unlock_file authorization: %s", v, b)
	}
	if strings.Contains(string(b), guardrailHolderCred) {
		t.Fatalf("check_guardrail leaked the credential value on the wire: %s", b)
	}
}
