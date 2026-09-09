package proxy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/wrongstack/wrongtrace/internal/ipc"
)

// keyRecordReporter is a minimal Reporter so this file does not depend on the
// shape of the shared fake in proxy_test.go.
type keyRecordReporter struct{}

func (keyRecordReporter) ReportRun(ipc.TelemetryReport) error { return nil }

// TestIsCredentialParam_KeyFamily covers the shape class that leaked before this
// fix: a query parameter whose canonical name ENDS IN the credential noun "key"
// without literally containing the enumerated spelling "apikey".
//
// Root cause of the leak: the qualified-shape loop matched the compound literal
// "apikey" instead of the bare noun, so every other *key spelling fell through
// and the upstream credential was written into ProxyTrafficRecord.TargetURL (and
// the proxy log, and the GET / status echo) in plaintext.
func TestIsCredentialParam_KeyFamily(t *testing.T) {
	mustRedact := []string{
		// The reported leak shapes, in each separator spelling.
		"subscription-key", "subscription_key", "SubscriptionKey", "subscriptionkey",
		"private_key", "private-key", "PrivateKey",
		"access_key", "access-key", "AccessKey",
		"secret_key", "security_key", "session_key", "user_key", "account_key",
		"ocp-apim-subscription-key",
		// Shapes already handled before this fix; pinned so they cannot regress.
		"key", "apikey", "api_key", "api-key", "x-api-key", "x-goog-api-key",
		"token", "access_token", "refresh-token", "bearer_token",
		"secret", "client_secret", "password", "authorization", "signature",
		"credential", "credentials", "auth",
	}
	for _, name := range mustRedact {
		if !isCredentialParam(name) {
			t.Errorf("isCredentialParam(%q) = false, want true (credential name would be recorded verbatim)", name)
		}
	}

	// Token-COUNT metadata and plain parameters must stay readable: the
	// exemption switch is ordered before the noun matching precisely so widening
	// the key family cannot re-introduce the prompt_tokens over-redaction that
	// E2E already caught once on the body path.
	mustStayReadable := []string{
		"max_tokens", "prompt_tokens", "completion_tokens", "total_tokens",
		"reasoning_tokens", "cached_tokens", "input_tokens", "output_tokens",
		"token_count", "cursor_token", "continuation_token",
		"model", "stream", "temperature", "api-version", "encoding_format",
	}
	for _, name := range mustStayReadable {
		if isCredentialParam(name) {
			t.Errorf("isCredentialParam(%q) = true, want false (non-credential metadata must stay readable)", name)
		}
	}
}

// TestIsCredentialParam_AgreesWithBodyPredicateOnKeyNames is the drift guard.
// isCredentialParam exists only because two hand-written lists had drifted, so
// the two predicates must not disagree on the name family this round fixed.
func TestIsCredentialParam_AgreesWithBodyPredicateOnKeyNames(t *testing.T) {
	for _, name := range []string{"key", "apikey", "api_key", "api-key", "x-api-key", "private_key"} {
		if body, record := isCredentialKey(name), isCredentialParam(name); body && !record {
			t.Errorf("predicate drift on %q: isCredentialKey=true but isCredentialParam=false (leaks into records)", name)
		}
	}
}

// TestSanitizeURLForRecord_KeyFamilyCredentialDoesNotSurvive asserts the leak on
// the function that actually guards the persisted surface, not just the predicate.
func TestSanitizeURLForRecord_KeyFamilyCredentialDoesNotSurvive(t *testing.T) {
	for _, param := range []string{"subscription-key", "private_key", "access_key", "api-key", "key"} {
		secret := fakeTok("zq", "9suf", "f1eld", "val", param[:3])
		in := "https://up.example.com/v1/chat/completions?" + param + "=" + secret + "&model=gpt-4o"

		got := sanitizeURLForRecord(in)

		if strings.Contains(got, secret) {
			t.Errorf("credential for %q survived sanitization: %q", param, got)
			continue
		}
		if v := redactedQueryValue(t, got, param); v != "[redacted]" {
			t.Errorf("record form for %q not redacted: value = %q in %q", param, v, got)
		}
		// The sibling non-credential parameter must survive untouched.
		if !strings.Contains(got, "model=gpt-4o") {
			t.Errorf("record form for %q clobbered a plain parameter: %q", param, got)
		}
	}
}

// redactedQueryValue reads a parameter back out of the record form. The
// redactor re-emits the query through url.Values.Encode(), so the marker arrives
// percent-encoded (%5Bredacted%5D) and a literal substring match can never hold.
func redactedQueryValue(t *testing.T, recordURL, param string) string {
	t.Helper()
	u, err := url.Parse(recordURL)
	if err != nil {
		t.Fatalf("parse record form %q: %v", recordURL, err)
	}
	return u.Query().Get(param)
}

// TestGatewayProxy_TrafficRecordDoesNotPersistKeyCredential drives the real
// ServeHTTP path against a live upstream so the assertion lands on the record
// the dashboard actually reads (ProxyTrafficRecord.TargetURL), not on a helper.
//
// An upstream that 404s or a closed port records nothing at all, so the proof
// would fail for setup reasons; this needs a listener that answers 200.
func TestGatewayProxy_TrafficRecordDoesNotPersistKeyCredential(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cmpl-1","model":"gpt-4o","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`))
	}))
	defer upstream.Close()

	secret := fakeTok("subscr", "iptk", "ey", "val", "7741")
	cases := []struct {
		name  string
		query string
	}{
		{"subscription-key", "subscription-key=" + secret},
		{"private_key", "private_key=" + secret},
		{"max_tokens stays readable", "max_tokens=64"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			records := make(chan ProxyTrafficRecord, 1)
			p := NewGatewayProxy(Config{
				Reporter:  keyRecordReporter{},
				OnTraffic: func(rec ProxyTrafficRecord) { records <- rec },
			})

			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
				strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
			req.Header.Set("Content-Type", "application/json")
			// Any auth value: without one the provider branch fails before a
			// traffic record is ever written.
			req.Header.Set("Authorization", fakeTok("c1", "ent", "aut", "h"))
			req.Header.Set("X-Target-Upstream", upstream.URL+"/v1?"+c.query)

			p.ServeHTTP(httptest.NewRecorder(), req)
			// Close drains the finalize worker, making the record deterministic.
			p.Close()

			var rec ProxyTrafficRecord
			select {
			case rec = <-records:
			default:
				t.Fatal("no traffic record captured; the production path was not exercised")
			}

			if c.name == "max_tokens stays readable" {
				if strings.Contains(rec.TargetURL, "max_tokens=[redacted]") {
					t.Errorf("token-count metadata was over-redacted in the record: %s", rec.TargetURL)
				}
				return
			}
			if strings.Contains(rec.TargetURL, secret) {
				t.Errorf("credential leaked into persisted traffic record TargetURL: %s", rec.TargetURL)
			}
			// Assert on the decoded parameter, not a literal marker substring.
			param := c.query[:strings.Index(c.query, "=")]
			if v := redactedQueryValue(t, rec.TargetURL, param); v != "[redacted]" {
				t.Errorf("persisted record not redacted for %q: value = %q in %s", param, v, rec.TargetURL)
			}
		})
	}
}
