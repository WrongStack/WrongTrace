package server

// Round: the lock ownership credential must not reach an agent through the
// guardrail read surfaces.
//
// owner_run_id is the credential Engine.UnlockFile verifies — presenting it is
// what tells the engine "this caller owns the lock". Rounds 60-62 redacted it
// from the IPC list_locks/file_health and MCP surfaces on the stated ground
// that "broadcasting it would let any agent steal locks", but the HTTP
// transports serialized the engine replies verbatim, so a caller that only
// asked "is it safe to edit this file?" was handed the authorization to unlock
// the file it was about to be refused from. That is the same
// per-surface-blind-spot regression the round-51 TTL rule warns about
// (01M1W8RWST1KGNNATCGE0DG8BD).

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const (
	credRegressionHeld   = "src/held_by_agent_a.go"
	credRegressionHolder = "cred-regression-holder-run"
)

func credRawGet(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200; body=%s", url, resp.StatusCode, b)
	}
	return string(b)
}

func credRawPost(t *testing.T, url, body string) (string, int) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return string(b), resp.StatusCode
}

// assertNoHolderCred fails when a reply carries the holder's credential, either
// as the serialized field or as its value.
func assertNoHolderCred(t *testing.T, body, surface, field string) {
	t.Helper()
	if strings.Contains(body, credRegressionHolder) {
		t.Fatalf("%s leaked the holder credential value on the wire: %s", surface, body)
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("%s: unparseable reply %q: %v", surface, body, err)
	}
	if v, present := m[field]; present && v != "" {
		t.Fatalf("%s leaked %s=%v; a guardrail read must not hand out unlock_file authorization: %s", surface, field, v, body)
	}
}

func TestGuardrailReadSurfacesRedactOwnerRunID(t *testing.T) {
	engine, _, ts := newTestServer(t)

	if _, err := engine.TryLockFile(credRegressionHeld, "refactor in progress", "agent-a", credRegressionHolder, time.Hour, false); err != nil {
		t.Fatalf("TryLockFile: %v", err)
	}
	// Precondition: the lock is really in force, so a later "no credential"
	// result is a redaction and not the trivial no-lock case.
	if locked, info := engine.IsFileLocked(credRegressionHeld); !locked || info.OwnerRunID != credRegressionHolder {
		t.Fatalf("precondition: IsFileLocked = %v/%q", locked, info.OwnerRunID)
	}

	t.Run("guardrail_check", func(t *testing.T) {
		body := credRawGet(t, ts.URL+"/api/guardrail/check?path="+credRegressionHeld)
		// The blocked verdict and the holder's NAME must survive, so the
		// redaction cannot be achieved by emptying the reply.
		if !strings.Contains(body, `"is_locked":true`) {
			t.Fatalf("guardrail check did not report the lock: %s", body)
		}
		if !strings.Contains(body, `"lock_owner":"agent-a"`) {
			t.Fatalf("guardrail check lost the human-readable holder: %s", body)
		}
		assertNoHolderCred(t, body, "/api/guardrail/check", "lock_owner_run_id")
	})

	t.Run("list_locks", func(t *testing.T) {
		body := credRawGet(t, ts.URL+"/api/guardrail/locks")
		if !strings.Contains(body, credRegressionHeld) || !strings.Contains(body, "agent-a") {
			t.Fatalf("list_locks lost the lock or its holder: %s", body)
		}
		// LockInfo serializes the field as owner_run_id here, not
		// lock_owner_run_id — assert on the shape this surface actually uses.
		if strings.Contains(body, credRegressionHolder) || strings.Contains(body, "owner_run_id") {
			t.Fatalf("list_locks leaked the holder credential on the wire: %s", body)
		}
	})

	t.Run("file_health", func(t *testing.T) {
		body := credRawGet(t, ts.URL+"/api/file/health?path="+credRegressionHeld)
		if !strings.Contains(body, `"is_locked":true`) {
			t.Fatalf("file/health did not report the lock: %s", body)
		}
		assertNoHolderCred(t, body, "/api/file/health", "lock_owner_run_id")
	})

	t.Run("lock_conflict_409", func(t *testing.T) {
		body, status := credRawPost(t, ts.URL+"/api/guardrail/lock",
			`{"path":"`+credRegressionHeld+`","owner":"agent-b","owner_run_id":"agent-b-own-run","ttl_seconds":60}`)
		if status != http.StatusConflict {
			t.Fatalf("contended lock returned %d, want 409; body=%s", status, body)
		}
		if !strings.Contains(body, "agent-a") {
			t.Fatalf("409 conflict lost the holder identity: %s", body)
		}
		assertNoHolderCred(t, body, "409 conflict", "owner_run_id")

		// Control: the redaction is scoped to the HOLDER. An uncontended lock
		// must still echo the CALLER's own credential back, or the owner could
		// never unlock its own lock.
		okBody, okStatus := credRawPost(t, ts.URL+"/api/guardrail/lock",
			`{"path":"src/owned_by_agent_b.go","owner":"agent-b","owner_run_id":"agent-b-own-run","ttl_seconds":60}`)
		if okStatus != http.StatusOK {
			t.Fatalf("uncontended lock returned %d, want 200; body=%s", okStatus, okBody)
		}
		if !strings.Contains(okBody, "agent-b-own-run") {
			t.Fatalf("success reply no longer returns the caller its own credential: %s", okBody)
		}
	})
}
