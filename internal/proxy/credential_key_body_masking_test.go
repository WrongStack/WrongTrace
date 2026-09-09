package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

// Regression: isCredentialKey enumerated vendor spellings of the "key" noun, so
// the body/header masking path (maskJSONValue -> ProxyTrafficRecord body previews
// and the IPC traffic view) persisted whole credentials whose name merely used a
// spelling nobody had listed. The exact switch already contained bare "key" and
// "private_key", while subscription_key (Azure's real form), access_key and
// user_key fell through to the fallback, whose only noun tests are "_token",
// "secret", "password", "credential", "api_key" and "apikey".
//
// The fix matches the credential nouns as whole name SEGMENTS -- split on
// separators and camelCase boundaries by credentialNameSegments -- ordered
// strictly AFTER the token-count exemption switch. It deliberately is NOT a
// substring or folded-name suffix test: a suffix match on the folded name masked
// `monkey`/`turkey`, a substring "key" match would redact `keys`/`keyboard`, and
// over-redacting usage metadata was itself a separate shipped regression here
// (E2E caught prompt_tokens being masked). Both directions are pinned below.

func TestIsCredentialKey_KeyFamilyIsRedacted(t *testing.T) {
	mustRedact := []string{
		// The shapes that leaked before the fix.
		"subscription_key", "subscription-key", "subscriptionKey",
		"access_key", "access-key", "accessKey", "user_key", "user-key",
		"secret_key", "security_key", "session_key", "account_key",
		"ocp-apim-subscription-key",
		// Shapes handled before the fix; pinned so they cannot regress.
		"key", "apikey", "api_key", "api-key", "x-api-key", "private_key", "private-key",
		"token", "access_token", "refresh-token", "session_token",
		"secret", "client_secret", "password", "authorization", "credentials",
	}
	for _, name := range mustRedact {
		if !isCredentialKey(name) {
			t.Errorf("isCredentialKey(%q) = false, want true (value would be persisted unmasked)", name)
		}
	}
}

func TestIsCredentialKey_UsageMetadataAndOrdinaryNamesStayReadable(t *testing.T) {
	// Preservation half of the contract: a widening that masks these repeats the
	// over-redaction bug this file's fix could easily have reintroduced.
	mustStayReadable := []string{
		"max_tokens", "prompt_tokens", "completion_tokens", "total_tokens",
		"max_completion_tokens", "reasoning_tokens", "cached_tokens",
		"input_tokens", "output_tokens", "token_count",
		// Names that only LOOK like the key family. Segment matching rejects all
		// of these because none yields a bare "key" segment -- `keys`, `keyboard`
		// and `keybindings` are single segments, and `monkey`/`turkey` merely end
		// in the letters "key". A folded-name suffix test matched the last two,
		// and a substring "key" match would have redacted the whole row.
		"keys", "keyboard", "keybindings", "monkey", "turkey",
		"model", "stream", "temperature", "tool_choice", "messages", "stop",
	}
	for _, name := range mustStayReadable {
		if isCredentialKey(name) {
			t.Errorf("isCredentialKey(%q) = true, want false (non-credential metadata must stay readable)", name)
		}
	}
}

// TestCredentialPredicates_AgreeOnKeyFamily is the two-directional drift guard.
// isCredentialKey (body/header masking) and isCredentialParam (record/log
// surface) exist as separate predicates but must never disagree on this name
// family: the original drift was recorded in the opposite direction.
func TestCredentialPredicates_AgreeOnKeyFamily(t *testing.T) {
	for _, name := range []string{
		"key", "apikey", "api_key", "api-key", "x-api-key", "private_key",
		"subscription_key", "subscription-key", "access_key", "user_key",
	} {
		body, record := isCredentialKey(name), isCredentialParam(name)
		if body != record {
			t.Errorf("predicate drift on %q: isCredentialKey=%v isCredentialParam=%v", name, body, record)
		}
	}
}

// TestMaskJSONValue_KeyFamilyCredentialIsRedacted drives the production masking
// function over a decoded payload, which is what the persisted record holds.
func TestMaskJSONValue_KeyFamilyCredentialIsRedacted(t *testing.T) {
	raw := `{"max_tokens":64,"model":"gpt-4o","subscription_key":"SUB9f2K","access_key":"ACC77Z","user_key":"USR11Q","nested":{"api_key":"OLD44","temperature":0.5}}`
	var decoded interface{}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("test bug: decode fixture: %v", err)
	}

	out, err := json.Marshal(maskJSONValue(decoded))
	if err != nil {
		t.Fatalf("test bug: marshal masked value: %v", err)
	}
	got := string(out)

	for _, secret := range []string{"SUB9f2K", "ACC77Z", "USR11Q", "OLD44"} {
		if strings.Contains(got, secret) {
			t.Errorf("credential %q survived body masking: %s", secret, got)
		}
	}
	// The field names must remain so the record stays diagnosable, and the
	// non-credential metadata must be untouched.
	for _, want := range []string{`"subscription_key"`, `"access_key"`, `"user_key"`, `"api_key"`} {
		if !strings.Contains(got, want) {
			t.Errorf("masked record lost field %s: %s", want, got)
		}
	}
	for _, want := range []string{`"max_tokens":64`, `"model":"gpt-4o"`, `"temperature":0.5`} {
		if !strings.Contains(got, want) {
			t.Errorf("masking clobbered non-credential metadata %s: %s", want, got)
		}
	}
}
