package proxy

// Regression (r98-shortredact): ScanAndRedactSecrets used to return early for
// any input shorter than 16 bytes. Two of its own patterns match below that
// floor — genericSecretRe needs only "passwd"+"="+8 value chars (15 bytes) and
// dbURLRe needs "redis://"+user+":"+pw+"@" (12 bytes) — and their pre-filter
// markers still fire for those inputs, so the guard alone stood between a
// complete credential and the caller. The redactor is invoked per SUBSTRING,
// not only on whole bodies: maskJSONValue scans each JSON string value and
// maskNonJSONBody scans each line, so a short value was stored in the
// dashboard-visible ProxyTrafficRecord unmasked. The floor is now derived from
// the patterns (minScanBytes = 12), and the record-path case below pins that
// consequence end to end.

import (
	"strings"
	"testing"
)

func TestScanAndRedactSecrets_ShortMatchableCredential(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		secret string
	}{
		{"generic assignment at the 15-byte floor", "passwd=abcd1234", "abcd1234"},
		{"db url credential at the 12-byte floor", "redis://a:b@", "://a:b@"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, n := ScanAndRedactSecrets([]byte(tc.in))
			if n == 0 || string(out) == tc.in || strings.Contains(string(out), tc.secret) {
				t.Fatalf("matchable credential survived redaction: in=%q out=%q redactions=%d", tc.in, string(out), n)
			}
		})
	}
}

func TestScanAndRedactSecrets_ShortControls(t *testing.T) {
	// Same shape one byte above the guard: redacted before and after the fix,
	// so a broken harness cannot masquerade as the defect.
	for _, in := range []string{" passwd=abcd1234", "redis://user:pass@"} {
		out, n := ScanAndRedactSecrets([]byte(in))
		if n == 0 || string(out) == in {
			t.Errorf("control shape not redacted: in=%q out=%q", in, string(out))
		}
	}
	// Below the derived floor nothing can match, so a clean body is untouched
	// byte for byte.
	clean := []byte("hello, this body carries no secret of any shape at all")
	out, n := ScanAndRedactSecrets(clean)
	if n != 0 || string(out) != string(clean) {
		t.Errorf("credential-free body mutated: out=%q redactions=%d", string(out), n)
	}
}

// TestSanitizeBodyRecord_ShortCredentialValueNotStored pins the reachable
// consequence: a VALID JSON body whose string value is a 15-byte matchable
// credential must not reach the stored traffic record.
func TestSanitizeBodyRecord_ShortCredentialValueNotStored(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"passwd=abcd1234"}]}`
	out := sanitizeBodyBytesForRecord([]byte(body))
	if strings.Contains(out, "abcd1234") {
		t.Fatalf("short credential reached the stored copy: %s", out)
	}
	if !strings.Contains(out, "[REDACTED_SECRET]") {
		t.Fatalf("expected the generic-secret placeholder in the stored copy: %s", out)
	}
}
