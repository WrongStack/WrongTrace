package proxy

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// TestComputeScopedKey_NoScopeBoundaryCollision pins the round-48 contract:
// distinct (provider, model, scope, body) tuples must produce distinct cache
// keys — no delimiter ambiguity. The pre-fix colon-joined form let a crafted
// model ("m:" + scope) collide with the honest (model, scope) pair (with
// scope'/body' split from the victim body at a colon), defeating the tenant
// isolation the scope digest exists to provide.
func TestComputeScopedKey_NoScopeBoundaryCollision(t *testing.T) {
	// Victim triple: honest model, own scope digest (requestCacheScope's
	// output shape), real request body.
	victimModel := "gpt-4o"
	victimScope := hex.EncodeToString([]byte{0xde, 0xad, 0xbe, 0xef})
	victimBody := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"internal confidential prompt"}]}`)

	victimKey := ComputeScopedKey("OpenAI", victimModel, victimScope, victimBody)

	// Crafted triple: model absorbs the scope boundary; scope'/body' are the
	// victim body split at its first colon so scope' + ":" + body' equals the
	// victim body byte-for-byte.
	attackerModel := victimModel + ":" + victimScope
	split := bytes.IndexByte(victimBody, ':')
	if split < 0 {
		t.Fatalf("setup: victim body has no ':' to split on")
	}
	attackerScope := string(victimBody[:split])
	attackerBody := victimBody[split+1:]

	attackerKey := ComputeScopedKey("OpenAI", attackerModel, attackerScope, attackerBody)
	if attackerKey == victimKey {
		t.Fatal("scope-boundary collision: a different (model, scope, body) triple produced the victim's exact cache key")
	}

	// Identity control: same triple, same key.
	if again := ComputeScopedKey("OpenAI", victimModel, victimScope, victimBody); again != victimKey {
		t.Fatalf("identity control broken: same inputs produced different keys (%s vs %s)", again, victimKey)
	}

	// Honest-scope control: distinct scopes never collide for the same model.
	a := ComputeScopedKey("OpenAI", victimModel, "scope-a", victimBody)
	b := ComputeScopedKey("OpenAI", victimModel, "scope-b", victimBody)
	if a == b {
		t.Fatal("honest-scope control broken: distinct scopes produced the same key")
	}

	// ComputeKey (empty scope) must stay distinct from any scoped key for the
	// same model/body.
	if unscoped := ComputeKey("OpenAI", victimModel, victimBody); unscoped == victimKey {
		t.Fatal("empty-scope key collided with the scoped key")
	}
}
