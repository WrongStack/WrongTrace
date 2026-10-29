package ipc

import "testing"

// Engine.UnlockFile reads an empty owner_run_id as a force unlock. An agent on
// this socket must not reach that path by omitting its credential, or it can
// release any other run's live lock. dispatch therefore never forwards "".
func TestDispatchUnlockFileWithoutCredentialIsNotForced(t *testing.T) {
	for _, method := range []string{"unlock_file", "guardrail/unlock", "telemetry/unlock_file"} {
		sink := &fakeSink{}
		srv := &Server{cfg: Config{Engine: sink}}
		resp := srv.dispatch(&Request{
			Method: method,
			Params: map[string]interface{}{"file_path": "src/important.go"},
		})
		if resp.Error != nil {
			t.Fatalf("%s: dispatch error: %v", method, resp.Error)
		}
		if sink.unlockCalls != 1 {
			t.Fatalf("%s: UnlockFile calls = %d, want 1", method, sink.unlockCalls)
		}
		if sink.unlockOwnerRunID == "" {
			t.Errorf("%s: forwarded an empty owner_run_id, which the engine treats as a force unlock", method)
		}
		if sink.unlockOwnerRunID != noCredentialRunID {
			t.Errorf("%s: ownerRunID = %q, want the no-credential marker", method, sink.unlockOwnerRunID)
		}
	}
}
