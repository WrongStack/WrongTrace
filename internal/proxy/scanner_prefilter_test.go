package proxy

import "testing"

// The OpenAI-key pre-filter must reject the everyday "sk-" substrings that
// used to force a full regex pass, and must never reject text the redaction
// regex would match.
func TestHasOpenAIKeyCandidate(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"run the task-runner and check disk-usage", false},
		{"risk-assessment for desk-booking", false},
		{"sk-short", false},
		{"key: sk-abcdefghijklmnopqrstuvwxyz0123", true},
		{"sk-abcdefghijklmnopqrst", true}, // at start of input
		{`"api_key":"sk-proj-ABCDEFGHIJKLMNOPQRSTUV"`, true},
		{"task-sk-abcdefghijklmnopqrstuvwxyz", true}, // '-' before "sk-" is a boundary
		{"xsk-abcdefghijklmnopqrstuvwxyz", false},
		{"sk-ant-api03-abcdefghijklmnopqrstu", true},
	}
	for _, c := range cases {
		got := hasOpenAIKeyCandidate([]byte(c.in))
		if got != c.want {
			t.Errorf("hasOpenAIKeyCandidate(%q) = %v, want %v", c.in, got, c.want)
		}
		// Soundness: anything the regex matches must pass the pre-filter.
		if openAIKeyRe.MatchString(c.in) && !got {
			t.Errorf("pre-filter rejected a regex match: %q", c.in)
		}
	}
}

func FuzzHasOpenAIKeyCandidateIsSound(f *testing.F) {
	for _, s := range []string{"sk-", "task-sk-aaaaaaaaaaaaaaaaaaaaaa", "x sk-aaaaaaaaaaaaaaaaaaaa-"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if openAIKeyRe.MatchString(s) && !hasOpenAIKeyCandidate([]byte(s)) {
			t.Fatalf("pre-filter rejected a regex match: %q", s)
		}
	})
}
