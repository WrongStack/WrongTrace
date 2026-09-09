package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestForwardedPathKeepsClientEscapes pins request-target fidelity for the PATH,
// the symmetric half of the query rule proven in query_forwarding_test.go.
//
// BUG: DetectProvider returned the DECODED url.URL.Path and both composition
// sites assigned it into base.Path, so url.String() re-escaped it. Decoding is
// lossy wherever an escape expands to a structural byte, and %2F is the case that
// matters: it becomes a real separator that no later step can restore, so ONE
// client path segment was forwarded as TWO
// ("/v1/models/meta-llama%2FLlama-3-8B" -> "/v1/models/meta-llama/Llama-3-8B"),
// and the provider was asked for a resource the client never named.
//
// It also misrouted the request, which is the worse half: isModelCatalogPath
// matches a trailing "models/<id>", and the invented extra segment broke that
// match, so a bodyless catalog GET fell through to the traced inference path --
// a phantom run, a traffic record, and a cache key (see the catalog-routing
// note in ServeHTTP). Both symptoms share the one root cause, so both are
// asserted here.
//
// The escapes that re-encode to themselves (%25, %3F, %23) are kept as explicit
// controls: they passed BEFORE this fix too, so they pin that the repair is
// scoped to the lossy case rather than a blanket "nothing was ever escaped"
// claim, and they fail loudly if the fix ever double-encodes ("%25" -> "%2525").
func TestForwardedPathKeepsClientEscapes(t *testing.T) {
	var mu sync.Mutex
	var got []string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.RequestURI) // exactly what arrived on the wire
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"cmpl-1","model":"m","data":[]}`)
	}))
	defer upstream.Close()

	p := NewGatewayProxy(Config{})
	defer p.Close()
	gw := httptest.NewServer(p)
	defer gw.Close()

	forward := func(t *testing.T, target, method, body string) string {
		t.Helper()
		mu.Lock()
		got = nil
		mu.Unlock()

		var rdr io.Reader
		if body != "" {
			rdr = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, gw.URL+target, rdr)
		if err != nil {
			t.Fatalf("NewRequest(%s): %v", target, err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Target-Upstream", upstream.URL)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request %s: %v", target, err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)

		mu.Lock()
		defer mu.Unlock()
		if len(got) != 1 {
			t.Fatalf("upstream received %d requests, want exactly 1 (target %s)", len(got), target)
		}
		return got[0]
	}

	cases := []struct {
		name   string
		target string
		method string
		body   string
	}{
		{"catalog relay keeps %2F", "/v1/models/meta-llama%2FLlama-3-8B", http.MethodGet, ""},
		{"inference path keeps %2F", "/v1/embeddings/org%2Fmodel", http.MethodPost, `{"model":"m","input":"hi"}`},
		{"%25 still survives (control)", "/v1/models/a%25b", http.MethodGet, ""},
		{"%3F still survives (control)", "/v1/models/a%3Fb", http.MethodGet, ""},
		{"%23 still survives (control)", "/v1/models/a%23b", http.MethodGet, ""},
		{"plain path unchanged (control)", "/v1/models/plain", http.MethodGet, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if arrived := forward(t, c.target, c.method, c.body); arrived != c.target {
				t.Errorf("upstream request-target = %q, want %q", arrived, c.target)
			}
		})
	}
}

// TestIsModelCatalogPathKeepsEncodedSlashInsideOneSegment pins the routing half
// of the same root cause directly, without going through the wire.
func TestIsModelCatalogPathKeepsEncodedSlashInsideOneSegment(t *testing.T) {
	// One segment named "meta-llama/Llama-3-8B": still models/<id>, so the call
	// stays an untraced transparent relay.
	if !isModelCatalogPath("v1/models/meta-llama%2FLlama-3-8B") {
		t.Errorf("models/<id> with an encoded slash was not classified as catalog; it would be traced as an inference run")
	}
	// The decoded form is what used to reach here and produced the extra segment.
	if isModelCatalogPath("v1/models/meta-llama/Llama-3-8B") {
		t.Errorf("the four-segment DECODED form unexpectedly classifies as catalog; this is the pre-fix shape, keep the assertion honest")
	}
}

// TestEncodedColonStillResolvesAndStaysTraced guards a regression that this
// round's OWN fix introduced, caught by measurement rather than review.
//
// Widening cleanPath to the escaped path hid every literal ":" that routing and
// classification depend on, because "%3A" is a legal encoding of a character
// that carries no structural meaning inside a segment. Two things broke:
// DetectProvider's embedded-URL passthrough ("proxy/https://host" and
// "host:port") stopped matching, and isModelCatalogPath's Gemini verb exclusion
// ("models/<id>:generateContent") stopped firing, which would have relayed a
// real inference call UNTRACED -- no run, no usage, no cost.
// normalizePathColons restores the colon at the single point where the path
// enters this function, so both consumers see exactly what they saw before.
//
// Asserted end to end through DetectProvider rather than by calling
// isModelCatalogPath with a raw "%3A" string: normalization lives at the source,
// so a direct classifier call with un-normalized input is not a state the
// product can reach, and pinning it would assert a contract that does not exist.
func TestEncodedColonStillResolvesAndStaysTraced(t *testing.T) {
	p := NewGatewayProxy(Config{})
	defer p.Close()

	// Routing: the encoded forms must resolve to the same provider and upstream
	// as the raw-colon forms a normal SDK sends.
	cases := []struct {
		wire             string
		wantProvider     string
		wantTargetSuffix string
		wantClean        string
	}{
		{"/proxy/https://api.z.ai/api/coding/paas/v4/chat/completions", "Z.AI", "api.z.ai", "/api/coding/paas/v4/chat/completions"},
		{"/proxy/https%3A//api.z.ai/api/coding/paas/v4/chat/completions", "Z.AI", "api.z.ai", "/api/coding/paas/v4/chat/completions"},
		{"/proxy/localhost:11434/v1/chat/completions", "Ollama", "localhost:11434", "/v1/chat/completions"},
		{"/proxy/localhost%3A11434/v1/chat/completions", "Ollama", "localhost:11434", "/v1/chat/completions"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, "http://proxy"+c.wire, nil)
		prov, target, cleanPath := p.DetectProvider(req)
		if prov != c.wantProvider || !strings.HasSuffix(target, c.wantTargetSuffix) {
			t.Errorf("DetectProvider(%q) = (%q, %q), want %q at a host ending %q",
				c.wire, prov, target, c.wantProvider, c.wantTargetSuffix)
		}
		if cleanPath != c.wantClean {
			t.Errorf("DetectProvider(%q) cleanPath = %q, want %q", c.wire, cleanPath, c.wantClean)
		}
	}

	// Classification: a Gemini inference call must stay on the traced path
	// whatever way its verb separator is encoded, while a "%2F" catalog read must
	// stay catalog. Both go through the real cleanPath DetectProvider produces.
	for _, wire := range []string{
		"/v1/models/gemini-2.5-pro:generateContent",
		"/v1/models/gemini-2.5-pro%3AgenerateContent",
		"/v1/models/gemini-2.5-pro%3agenerateContent",
	} {
		req := httptest.NewRequest(http.MethodPost, "http://proxy"+wire, nil)
		req.Header.Set("X-Target-Upstream", "http://127.0.0.1:1/v1")
		_, _, cleanPath := p.DetectProvider(req)
		if isModelCatalogPath(cleanPath) {
			t.Errorf("wire %q classified as catalog via cleanPath %q; a real inference call would be relayed untraced", wire, cleanPath)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "http://proxy/v1/models/meta-llama%2FLlama-3", nil)
	req.Header.Set("X-Target-Upstream", "http://127.0.0.1:1/v1")
	if _, _, cleanPath := p.DetectProvider(req); !isModelCatalogPath(cleanPath) {
		t.Errorf("the encoded-slash catalog read stopped classifying as catalog via cleanPath %q", cleanPath)
	}
}

// TestForwardedPathKeepsConfiguredBaseEscapes pins escape fidelity for the
// CONFIGURED upstream base, which is a different input from the client path the
// tests above cover. Reported at the end of round 7 as a residual left "untouched
// and not fixed"; that was wrong in the conservative direction, because
// setForwardedPath derives the base prefix from u.EscapedPath() and url.Parse
// populates RawPath for a base such as ".../prefix%2Fseg". Measured against HEAD
// (after round 1's query fix, before round 7's path fix) every escape row here
// FAILS -- "/prefix%2Fseg/v1/..." arrives as "/prefix/seg/v1/..." -- so this is a
// real pin rather than decoration. It also covers BOTH composition sites: an
// inference path (ServeHTTP) and a catalog relay (relayCatalogRequest).
func TestForwardedPathKeepsConfiguredBaseEscapes(t *testing.T) {
	var mu sync.Mutex
	var got string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = r.RequestURI
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"cmpl-1","model":"m","data":[]}`)
	}))
	defer upstream.Close()

	p := NewGatewayProxy(Config{})
	defer p.Close()
	gw := httptest.NewServer(p)
	defer gw.Close()

	cases := []struct {
		name string
		base string
		path string
	}{
		{"encoded slash in the base, inference site", upstream.URL + "/prefix%2Fseg", "/v1/chat/completions"},
		{"encoded slash in the base, catalog relay site", upstream.URL + "/prefix%2Fseg", "/v1/models/deepseek-chat"},
		{"encoded percent and slash together in the base", upstream.URL + "/a%25b%2Fc", "/v1/chat/completions"},
		{"plain base stays as it was (control)", upstream.URL + "/v1", "/chat/completions"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// The base's own path is everything after the host, so the expected
			// request target is that path plus the forwarded client path.
			want := strings.TrimPrefix(c.base, upstream.URL) + c.path

			mu.Lock()
			got = ""
			mu.Unlock()

			req, err := http.NewRequest(http.MethodGet, gw.URL+c.path, nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Target-Upstream", c.base)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("GET %s: %v", c.path, err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()

			mu.Lock()
			arrived := got
			mu.Unlock()
			if arrived != want {
				t.Errorf("upstream request-target = %q, want %q (base %q + path %q)", arrived, want, c.base, c.path)
			}
		})
	}
}

// TestDetectProviderReturnsEscapedPath pins the widened contract itself: the
// exported third value is the caller's escaped path, which is what both
// composition sites need to stay byte-faithful. ASCII paths are unaffected
// (EscapedPath() == Path), so this is the line a future "tidy-up" must not cross.
func TestDetectProviderReturnsEscapedPath(t *testing.T) {
	p := NewGatewayProxy(Config{})
	defer p.Close()

	req := httptest.NewRequest(http.MethodGet, "/v1/models/org%2Fmodel", nil)
	req.Header.Set("X-Target-Upstream", "http://127.0.0.1:1/v1")
	if _, _, cleanPath := p.DetectProvider(req); cleanPath != "/v1/models/org%2Fmodel" {
		t.Errorf("DetectProvider cleanPath = %q, want the escaped %q", cleanPath, "/v1/models/org%2Fmodel")
	}

	ascii := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	ascii.Header.Set("X-Target-Upstream", "http://127.0.0.1:1/v1")
	if _, _, cleanPath := p.DetectProvider(ascii); cleanPath != "/v1/chat/completions" {
		t.Errorf("ASCII path changed shape: cleanPath = %q", cleanPath)
	}
}
