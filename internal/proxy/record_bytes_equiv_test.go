package proxy

import (
	"strings"
	"testing"
)

// sanitizeBodyBytesForRecord cuts oversized wire bodies before converting
// them to strings; its output must match sanitizing the whole body as a
// string, including at rune boundaries and around the truncation threshold.
func TestSanitizeBodyBytesMatchesStringPath(t *testing.T) {
	jsonBody := `{"messages":[{"role":"user","content":"` + strings.Repeat("ç", maxRecordBodyLen) + `"}],"api_key":"sk-secret-value-1234567890"}`
	sse := strings.Repeat("data: {\"choices\":[{\"delta\":{\"content\":\"héllo wörld\"}}]}\n\n", 4000)
	cases := map[string]string{
		"empty":             "",
		"small json":        `{"model":"m","authorization":"Bearer abc"}`,
		"at threshold":      strings.Repeat("a", maxRecordBodyLen*2),
		"just over":         strings.Repeat("a", maxRecordBodyLen*2+1),
		"multibyte over":    strings.Repeat("€", maxRecordBodyLen),
		"large json":        jsonBody,
		"large sse":         sse,
		"mixed rune offset": "x" + strings.Repeat("日本", maxRecordBodyLen/2) + "y",
	}
	for name, body := range cases {
		want := sanitizeBodyForRecord(body)
		if got := sanitizeBodyBytesForRecord([]byte(body)); got != want {
			t.Errorf("%s: bytes path diverged (len got=%d want=%d)", name, len(got), len(want))
		}
	}
}
