package proxy

// Regression (r-cutmask-straddle): both record sanitizers used to cut
// oversized bodies to head(3/4)+tail(1/4) BEFORE masking. A credential
// straddling either cut then reached the stored copy as a partial fragment
// the complete-pattern credential regexes cannot match — an AWS key cut 18
// bytes in left "AKIAIOSFODNN7EXAM" (18 of its 20 characters) in the
// dashboard-visible traffic record, and a tail-side cut left the last 12
// characters. The sanitizers now take the mask-first path whenever any
// credential marker fires (bodyMayContainCredentials), so only marker-free
// bodies are cut before masking: nothing credential-shaped can straddle a
// seam, because every credential regex match implies its pre-filter marker
// fires (kept in sync with ScanAndRedactSecrets's gate).
//
// Fixture credential: the AWS documentation example access key (a canonical
// fake), assembled from byte values so the source cannot degenerate into a
// redaction placeholder. Keys are comma-delimited so the intact key satisfies
// awsKeyRe's word boundaries (the scanner's standing contract), and the JSON
// fixture is valid so the mask-first path walks it through maskJSONValue,
// which scans the full string before truncating it.

import (
	"strings"
	"testing"
)

func TestSanitizeBodyRecordSeamDoesNotLeakCredentialFragments(t *testing.T) {
	key := string([]byte{0x41, 0x4B, 0x49, 0x41}) + "IOSFODNN7EXAMPLE"
	cut := maxRecordBodyLen * 3 / 4
	retained := 18
	partial := key[:retained]

	// Case 1: head seam — the cut lands 18 bytes into the key.
	body1 := strings.Repeat(`"pad",`, (cut-retained)/6) + key + strings.Repeat(`"pad",`, 26000)
	out1 := sanitizeBodyBytesForRecord([]byte(body1))
	if strings.Contains(out1, partial) || strings.Contains(out1, key) {
		t.Errorf("head-seam credential fragment reached the stored copy")
	}

	// Case 2: control — key fully inside the head, comma-delimited. Proves the
	// masking machinery and the assertions work when no seam is involved.
	body2 := strings.Repeat(`"pad",`, 8185) + key + strings.Repeat(`"pad",`, 26000)
	out2 := sanitizeBodyBytesForRecord([]byte(body2))
	if !strings.Contains(out2, "[REDACTED_AWS_KEY]") || strings.Contains(out2, key) {
		t.Errorf("control broken: complete credential not masked")
	}

	// Case 3: valid JSON body whose content string carries the key across the
	// seam — mask-first must walk it through maskJSONValue.
	prefix := `{"messages":[{"role":"user","content":"`
	body3 := prefix + strings.Repeat("m", cut-retained-len(prefix)-1) + "," + key + "," + strings.Repeat("m", 150000) + "\"}]}"
	out3 := sanitizeBodyBytesForRecord([]byte(body3))
	if strings.Contains(out3, partial) || strings.Contains(out3, key) {
		t.Errorf("json seam credential fragment reached the stored copy")
	}

	// Case 4: byte path and string path agree on the hazard shape.
	if out4 := sanitizeBodyForRecord(string(body1)); out4 != out1 {
		t.Errorf("byte and string sanitizer paths diverge on the seam shape")
	}

	// Case 5: tail seam — tailStart lands 8 bytes into the key, so the
	// pre-fix retained tail held the key's last 12 characters.
	total5 := 200000
	tailStart5 := total5 - maxRecordBodyLen/4
	credStart5 := tailStart5 - 8
	body5 := strings.Repeat("a", credStart5-1) + "," + key + "," + strings.Repeat("b", total5-credStart5-len(key)-1)
	out5 := sanitizeBodyBytesForRecord([]byte(body5))
	if strings.Contains(out5, key[8:]) || strings.Contains(out5, key) {
		t.Errorf("tail-seam credential fragment reached the stored copy")
	}
}
