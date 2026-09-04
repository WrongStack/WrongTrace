package proxy

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// FuzzScanAndRedactSecrets exercises the redactor on arbitrary bytes.
//
// This runs on every request body forwarded through the gateway, so it sees
// whatever an agent sends -- including binary, invalid UTF-8, and payloads
// crafted to sit exactly on a pattern boundary. Two properties are asserted:
// it must never panic, and it must be a fixed point. A redactor that keeps
// changing its own output on re-application is silently corrupting payloads
// somewhere in its index arithmetic.
func FuzzScanAndRedactSecrets(f *testing.F) {
	for _, seed := range []string{
		"",
		"short",
		`{"prompt":"hello world, nothing secret here"}`,
		`{"key":"sk-abcdefghijklmnopqrstuvwxyz012345"}`,
		`{"key":"sk-ant-abcdefghijklmnopqrstuvwxyz01"}`,
		`AKIAIOSFODNN7EXAMPLE`,
		`ghp_0123456789abcdefghijklmnopqrstuvwxyz`,
		`AIzaSyA012345678901234567890123456789ab`,
		`postgres://user:hunter2@localhost:5432/db`,
		`password = "correcthorsebattery"`,
		`PASSWORD: supersecret123`,
		"-----BEGIN RSA PRIVATE KEY-----\nMIIBOgIBAAJBAK\n-----END RSA PRIVATE KEY-----",
		// Boundary shapes: just under/over the length floors the regexes use.
		`sk-tooshort`,
		`AKIASHORT`,
		`password=short`,
		"sk-\x00\xff\xfe invalid utf8 after the prefix",
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		once, n1 := ScanAndRedactSecrets(body)

		if n1 == 0 && !bytes.Equal(once, body) {
			t.Fatalf("reported 0 redactions but mutated the body:\nin:  %q\nout: %q", body, once)
		}

		twice, _ := ScanAndRedactSecrets(once)
		if !bytes.Equal(once, twice) {
			t.Fatalf("redaction is not a fixed point; re-applying changed the output again:\n"+
				"in:     %q\nonce:   %q\ntwice:  %q", body, once, twice)
		}
	})
}

// TestScanAndRedactSecrets_ActuallyRedacts is the assertion fuzzing cannot make:
// that real credential shapes do not survive. A regex that silently stops
// matching would leave the fuzz properties above perfectly satisfied while
// leaking every key an agent pastes into a prompt.
func TestScanAndRedactSecrets_ActuallyRedacts(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		secret string
	}{
		{"aws access key", `{"env":"AKIAIOSFODNN7EXAMPLE is the key"}`, "AKIAIOSFODNN7EXAMPLE"},
		{"github classic token", `token: ghp_0123456789abcdefghijklmnopqrstuvwxyz`,
			"ghp_0123456789abcdefghijklmnopqrstuvwxyz"},
		{"openai key", `{"k":"sk-abcdefghijklmnopqrstuvwxyz012345"}`,
			"sk-abcdefghijklmnopqrstuvwxyz012345"},
		{"anthropic key", `{"k":"sk-ant-abcdefghijklmnopqrstuvwxyz01"}`,
			"sk-ant-abcdefghijklmnopqrstuvwxyz01"},
		{"google api key", `key=AIzaSyA012345678901234567890123456789ab`,
			"AIzaSyA012345678901234567890123456789ab"},
		{"db url password", `postgres://user:hunter2secret@localhost:5432/db`, "hunter2secret"},
		{"generic password", `password = "correcthorsebattery"`, "correcthorsebattery"},
		{"private key block",
			"-----BEGIN RSA PRIVATE KEY-----\nMIIBOgIBAAJBAKsecretmaterial\n-----END RSA PRIVATE KEY-----",
			"MIIBOgIBAAJBAKsecretmaterial"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, n := ScanAndRedactSecrets([]byte(tc.body))
			if n == 0 {
				t.Fatalf("reported no redactions for a body containing a %s", tc.name)
			}
			if strings.Contains(string(out), tc.secret) {
				t.Errorf("secret survived redaction.\nbody: %s\nout:  %s", tc.body, out)
			}
		})
	}
}

// TestScanAndRedactSecrets_PreservesJSONValidity guards the bug fuzzing found:
// the pattern consumes the quote delimiting a secret in order to locate it, and
// replacing the whole match threw that quote away. A secret pasted into a
// prompt therefore came out as {"content":"PASSWORD= [REDACTED_SECRET]} --
// malformed JSON, which the gateway then forwarded to the provider, which
// rejected it. The protection broke the very request it was protecting.
func TestScanAndRedactSecrets_PreservesJSONValidity(t *testing.T) {
	cases := []string{
		`{"messages":[{"role":"user","content":"here is my env: PASSWORD=hunter2secret"}]}`,
		`{"messages":[{"role":"user","content":"auth_token = abcdefghijkl"}]}`,
		`{"c":"password: 'abcdefghijkl'"}`,
		`{"c":"client_secret: abcdefghijkl"}`,
		`{"a":"passwd=abcdefghijkl","b":"api_secret=mnopqrstuvwx"}`,
	}
	for _, in := range cases {
		out, n := ScanAndRedactSecrets([]byte(in))
		if !json.Valid([]byte(in)) {
			t.Fatalf("test fixture is not valid JSON to begin with: %s", in)
		}
		if n == 0 {
			t.Errorf("no redaction for %s", in)
			continue
		}
		if !json.Valid(out) {
			t.Errorf("redaction produced invalid JSON:\n  in:  %s\n  out: %s", in, out)
		}
		if bytes.Contains(out, []byte("hunter2secret")) || bytes.Contains(out, []byte("abcdefghijkl")) {
			t.Errorf("secret survived: %s", out)
		}
	}
}

// TestScanAndRedactSecrets_IsIdempotent: re-scanning already-redacted content
// must be a no-op. It was not -- the placeholder is bracketed and the secret
// character class admits brackets, so the redactor matched its own output and
// rewrote it, dropping trailing characters each time.
func TestScanAndRedactSecrets_IsIdempotent(t *testing.T) {
	in := []byte(`{"c":"PASSWORD=hunter2secret and passwd=abcdefghijkl"}`)

	once, n1 := ScanAndRedactSecrets(in)
	if n1 == 0 {
		t.Fatal("fixture was not redacted at all")
	}
	twice, n2 := ScanAndRedactSecrets(once)
	if !bytes.Equal(once, twice) {
		t.Errorf("second pass changed the output again:\n  once:  %s\n  twice: %s", once, twice)
	}
	if n2 != 0 {
		t.Errorf("second pass reported %d redactions on already-redacted content, want 0", n2)
	}
}

// TestScanAndRedactSecrets_LeavesCleanBodiesAlone: over-redaction is its own
// bug. The gateway forwards this output to the provider, so a false positive
// corrupts a legitimate request rather than merely logging noise.
func TestScanAndRedactSecrets_LeavesCleanBodiesAlone(t *testing.T) {
	for _, body := range []string{
		`{"messages":[{"role":"user","content":"explain how sk- prefixes work"}]}`,
		`{"model":"gpt-4","temperature":0.7,"max_tokens":1000}`,
		`{"content":"the AKIA prefix identifies AWS access key IDs"}`,
		`{"content":"connect with postgres://localhost:5432/db"}`,
	} {
		out, n := ScanAndRedactSecrets([]byte(body))
		if n != 0 {
			t.Errorf("redacted a clean body (%d hits): %s -> %s", n, body, out)
		}
	}
}
