package proxy

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// FuzzDetectProvider drives the whole provider-resolution chain on arbitrary
// request paths. Resolution walks the path with SplitN/TrimPrefix and slices
// the first byte of hostname fragments to build a display label -- index
// arithmetic on caller-controlled input, on the request path, where a panic
// takes the gateway down.
func FuzzDetectProvider(f *testing.F) {
	for _, p := range []string{
		"/", "//", "/proxy/", "/proxy//", "/proxy/openai/v1/chat/completions",
		"/proxy/https://api.z.ai/v4/chat", "/proxy/https:/api.z.ai/v4/chat",
		"/proxy/http://localhost:11434/v1/chat", "/proxy/api.groq.com/openai/v1",
		"/proxy/localhost:11434", "/proxy/.", "/proxy/..", "/proxy/:",
		"/proxy/https://", "/proxy/http:/", "/v1/chat/completions",
		"/openrouter/v1", strings.Repeat("/a", 200), "/proxy/" + strings.Repeat(".", 100),
	} {
		f.Add(p)
	}

	f.Fuzz(func(t *testing.T, path string) {
		// httptest.NewRequest panics on a target it cannot parse at all; that is
		// the test harness's constraint, not the code under test.
		if path == "" || path[0] != '/' {
			t.Skip()
		}
		req, err := http.NewRequest(http.MethodPost, "http://example.com"+path, nil)
		if err != nil {
			t.Skip()
		}

		p := NewGatewayProxy(Config{CustomUpstreams: map[string]string{
			"custom":        "https://custom.example.com",
			"custom-longer": "https://longer.example.com",
			"default":       "https://fallback.example.com",
		}})
		defer p.Close()

		_, _, _ = p.DetectProvider(req)
	})
}

// FuzzSanitizeURLForRecord covers the URL redactor applied to every recorded
// request. Its whole job is to keep query-string credentials out of stored
// traffic, so it must neither panic nor pass a known-sensitive parameter
// through unredacted.
func FuzzSanitizeURLForRecord(f *testing.F) {
	for _, u := range []string{
		"", "http://x/y", "https://api.example.com/v1?key=secret123",
		"https://api.example.com/v1?api_key=abc&other=ok",
		"https://x/?token=t&access_token=a&signature=s&secret=z",
		"https://x/?PASSWORD=p", "://", "?", "?key=", "%%%", "http://%zz",
		"https://x/?key=a?key=b", strings.Repeat("?", 100),
	} {
		f.Add(u)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		out := sanitizeURLForRecord(raw)

		// If the input carried a credential parameter with a non-empty value
		// and the URL was parseable, that value must not survive verbatim.
		u, err := parseForFuzz(raw)
		if err != nil || u == nil {
			return
		}
		for k, vs := range u.Query() {
			if !strings.EqualFold(k, "key") && !strings.EqualFold(k, "api_key") &&
				!strings.EqualFold(k, "token") && !strings.EqualFold(k, "access_token") {
				continue
			}
			for _, v := range vs {
				// Short values can legitimately reappear as substrings of the
				// rest of the URL; only assert on values distinctive enough to
				// make a coincidental match implausible.
				if len(v) >= 12 && strings.Contains(out, v) {
					t.Fatalf("credential value %q survived sanitization of %q -> %q", v, raw, out)
				}
			}
		}
	})
}

// parseForFuzz mirrors sanitizeURLForRecord's own parse step so the fuzz
// property only asserts on URLs the function actually understood.
func parseForFuzz(raw string) (*url.URL, error) { return url.Parse(raw) }
