package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// queryRecorder is a mock upstream that captures the raw query string and path
// it actually received on the wire.
type queryRecorder struct {
	mu       sync.Mutex
	rawQuery string
	path     string
}

func (q *queryRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q.mu.Lock()
	q.rawQuery, q.path = r.URL.RawQuery, r.URL.Path
	q.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"id":"chatcmpl-q","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
}

func (q *queryRecorder) snapshot() (string, string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.rawQuery, q.path
}

// forwardQuery drives one request through the real gateway over an HTTP
// connection and returns what the upstream observed.
func forwardQuery(t *testing.T, method, target, upstream string) (string, string) {
	t.Helper()
	rec := &queryRecorder{}
	up := httptest.NewServer(rec)
	defer up.Close()

	// Callers pass a literal "%s" placeholder for the upstream base.
	if strings.Contains(upstream, "%s") {
		upstream = strings.ReplaceAll(upstream, "%s", up.URL)
	} else {
		upstream = up.URL
	}

	p := NewGatewayProxy(Config{})
	ps := httptest.NewServer(p)
	defer ps.Close()

	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader(`{"model":"gpt-4o"}`)
	}
	req, err := http.NewRequest(method, strings.ReplaceAll(target, "%s", ps.URL), body)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Target-Upstream", upstream)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s through proxy: %v", method, err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("proxy returned %d", resp.StatusCode)
	}
	return rec.snapshot()
}

// TestGatewayProxy_ForwardsClientQueryByteForwards pins the fidelity contract
// of the transparent gateway: the caller's query string must reach the
// upstream UNCHANGED, because re-escaping it silently rewrites real values.
//
// BUG (regression introduced while fixing the swallowed-path bug): the merge
// parsed the incoming wire query into url.Values with Set() and re-emitted it
// with Encode(). url.Values holds DECODED values and Encode() escapes again, so
// already-encoded components were escaped a second time ("%2F" -> "%252F",
// "%25" -> "%2525"), a wire "+" (which means space) became a literal "%2B",
// repeated keys collapsed because Set() replaces rather than appends, and
// Encode() reordered parameters alphabetically -- which invalidates pre-signed
// upstream URLs.
//
// FIX: join the raw query strings. base.RawQuery keeps any credential embedded
// in the configured upstream (e.g. Gemini "?key="), and the client's bytes are
// forwarded verbatim, so both behaviours hold at once.
func TestGatewayProxy_ForwardsClientQueryByteForwards(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"percent escapes survive intact", "filter=name%2Fvalue&path=a%25b"},
		{"plus keeps its space semantics", "q=a+b"},
		{"repeated parameters are preserved", "tag=one&tag=two&tag=three"},
		{"parameter order is preserved", "zeta=1alpha=2&mid=3"},
		{"unreserved and reserved mix", "a=b~c!d$e&f=g'h(i)j"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotRaw, gotPath := forwardQuery(t, http.MethodPost, "%s/v1/chat/completions?"+tc.want, "")
			if gotRaw != tc.want {
				t.Errorf("upstream RawQuery = %q, want %q (path %q)", gotRaw, tc.want, gotPath)
			}
			if strings.Contains(gotRaw, "%252") {
				t.Errorf("double-encoded escape sequence reached upstream: %q", gotRaw)
			}
		})
	}
}

// TestGatewayProxy_EmbeddedUpstreamKeyStillForwarded guards the behaviour the
// raw-query merge must not lose: a credential carried by the configured base is
// still sent upstream, and the API path still lands in the PATH component
// instead of being absorbed into the query string.
func TestGatewayProxy_EmbeddedUpstreamKeyStillForwarded(t *testing.T) {
	key := fakeTok("query", "credential", "123")
	gotRaw, gotPath := forwardQuery(t, http.MethodPost, "%s/v1/chat/completions?alt=json", "%s/?key="+key)

	if !strings.Contains(gotRaw, "key="+key) {
		t.Errorf("embedded upstream credential was not forwarded: %q", gotRaw)
	}
	if !strings.Contains(gotRaw, "alt=json") {
		t.Errorf("client query was dropped: %q", gotRaw)
	}
	// The pre-merge bug put the path after '?' so the upstream saw "/" only.
	if gotPath != "/v1/chat/completions" {
		t.Errorf("forwarded path = %q, want %q", gotPath, "/v1/chat/completions")
	}
}

// TestSanitizeURLForRecord_RedactionCollidesDistinctCredentials proves the
// reason sanitizeURLForRecord can never become a cache key: redaction is lossy
// and COLLAPSING. Two upstream URLs that differ only in their credential
// sanitize to one identical string, so keying a cache (or forwarding a request)
// on the record form would serve one route's response to another route's
// caller, and would drop the credential query-auth providers need.
// See the CONTRACT comment on sanitizeURLForRecord.
func TestSanitizeURLForRecord_RedactionCollidesDistinctCredentials(t *testing.T) {
	const model = "gpt-4o"
	a := sanitizeURLForRecord("https://up.example/v1/c?model=" + model + "&key=" + fakeTok("aaa", "KEY", "1"))
	b := sanitizeURLForRecord("https://up.example/v1/c?model=" + model + "&key=" + fakeTok("bbb", "KEY", "2"))

	if a != b {
		t.Fatalf("distinct credentials did not collide: %q vs %q", a, b)
	}
	for _, secret := range []string{fakeTok("aaa", "KEY", "1"), fakeTok("bbb", "KEY", "2")} {
		if strings.Contains(a, secret) {
			t.Errorf("record form still contains credential %q: %q", secret, a)
		}
	}
	// The collision is only safe because this string is never forwarded. The
	// real forwarded query is pinned byte-for-byte by
	// TestGatewayProxy_ForwardsClientQueryByteForwards.
	if !strings.Contains(a, "model="+model) {
		t.Errorf("non-credential parameter lost from record: %q", a)
	}
}

// TestSanitizeURLForRecord_PinsRecordSurfaceSemantics pins the exact
// normalization contract the CONTRACT comment describes, so a future edit that
// quietly changes it (in either direction) has to update this test on purpose.
func TestSanitizeURLForRecord_PinsRecordSurfaceSemantics(t *testing.T) {
	t.Run("no credential means byte-identical passthrough", func(t *testing.T) {
		in := "https://up.example/v1/c?zeta=1&alpha=2&filter=a%2Fb&sig=x%2By"
		if got := sanitizeURLForRecord(in); got != in {
			t.Errorf("record form = %q, want verbatim %q", got, in)
		}
	})

	t.Run("redacting branch normalizes and is not byte-faithful", func(t *testing.T) {
		in := "https://up.example/v1/c?zeta=1&alpha=2&key=" + fakeTok("sek", "RET")
		got := sanitizeURLForRecord(in)
		if strings.Contains(got, fakeTok("sek", "RET")) {
			t.Errorf("credential survived: %q", got)
		}
		// Alphabetical: alpha before zeta, which is why this form must never be
		// used to build a forwarded URL.
		if !strings.Contains(got, "alpha=2") || strings.Index(got, "alpha=2") > strings.Index(got, "zeta=1") {
			t.Errorf("parameters not sorted as documented: %q", got)
		}
	})

	t.Run("invalid percent escape drops the parameter", func(t *testing.T) {
		// Documented sharp edge: url.Values never sees a broken pair, so it
		// disappears from the record entirely.
		got := sanitizeURLForRecord("https://up.example/v1/c?broken=%zz&key=" + fakeTok("sek", "RET"))
		if strings.Contains(got, "broken") {
			t.Errorf("expected the unparseable parameter to be dropped, got %q", got)
		}
		if !strings.Contains(got, "key=") {
			t.Errorf("credential parameter missing from record: %q", got)
		}
	})

	// Deliberately branch-agnostic: a space in the authority makes url.Parse
	// fail (fallback returns the path plus "?[redacted]"), but the credential
	// must be absent either way, so this pins the leak guarantee rather than
	// which code path produced it.
	t.Run("malformed input never leaks the credential", func(t *testing.T) {
		got := sanitizeURLForRecord("http://ex ample/v1/c?key=" + fakeTok("sek", "RET"))
		if strings.Contains(got, fakeTok("sek", "RET")) {
			t.Errorf("record form leaked the credential: %q", got)
		}
	})
}

// TestGatewayProxy_CatalogRelayForwardsQueryUnchanged covers the second call
// site: relayCatalogRequest is documented as a transparent, untraced relay, so
// a model-catalog call's query bytes must survive unchanged as well.
func TestGatewayProxy_CatalogRelayForwardsQueryUnchanged(t *testing.T) {
	want := "limit=5&page_token=ab%2Bcd&tag=one&tag=two"
	gotRaw, gotPath := forwardQuery(t, http.MethodGet, "%s/v1/models?"+want, "")
	if gotRaw != want {
		t.Errorf("catalog relay changed the query: got %q, want %q", gotRaw, want)
	}
	if !strings.HasSuffix(gotPath, "/v1/models") {
		t.Errorf("catalog relay changed the path: %q", gotPath)
	}
}
