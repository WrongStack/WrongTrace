package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestDetectProvider_OverlappingAliasesAreDeterministic guards the root cause:
// the CustomUpstreams fallback scanned `for k := range p.providers`, and Go
// randomizes map iteration order. A path containing two registered aliases
// ("openai" is a substring of "openai-compat") therefore routed to a different
// upstream on every request — traffic silently split across two vendors and
// the recorded provider name flipped between records for identical calls.
// Resolution is now most-specific-first: the longest matching alias wins.
func TestDetectProvider_OverlappingAliasesAreDeterministic(t *testing.T) {
	p := NewGatewayProxy(Config{CustomUpstreams: map[string]string{
		"openai-compat": "https://compat.example.com",
	}})
	defer p.Close()

	req := httptest.NewRequest(http.MethodPost, "/openai-compat/v1/chat/completions", nil)

	// One pass would pick the right upstream by luck; the bug only shows over
	// repeated resolution, because each iteration reseeds the map order.
	for i := 0; i < 200; i++ {
		_, target, _ := p.DetectProvider(req)
		if target != "https://compat.example.com" {
			t.Fatalf("iteration %d resolved to %q, want the longest matching alias "+
				"(https://compat.example.com); alias resolution is order-dependent again", i, target)
		}
	}
}

// TestDetectProvider_DefaultIsNotMatchedAsSubstring pins the other half: the
// "default" key is the explicit catch-all, not an alias to look for inside the
// path. Scanning it as a substring made any request whose URL happened to
// contain the word "default" resolve to the fallback upstream ahead of a real
// alias that also matched.
func TestDetectProvider_DefaultIsNotMatchedAsSubstring(t *testing.T) {
	p := NewGatewayProxy(Config{CustomUpstreams: map[string]string{
		"default": "https://fallback.example.com",
	}})
	defer p.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/models/default-model/mistral", nil)
	_, target, _ := p.DetectProvider(req)
	if target != "https://api.mistral.ai" {
		t.Fatalf("resolved to %q, want the real alias https://api.mistral.ai; "+
			"the default sentinel was matched as a path substring", target)
	}
}

// TestDetectProvider_AliasNameIsProperlyCased documents the display side: the
// resolved provider label used to come from the deprecated strings.Title, which
// rendered "openrouter" as "Openrouter". Labels now go through the same
// prettifier every other resolution branch uses.
func TestDetectProvider_AliasNameIsProperlyCased(t *testing.T) {
	p := NewGatewayProxy(Config{})
	defer p.Close()

	req := httptest.NewRequest(http.MethodPost, "/openrouter/v1/chat/completions", nil)
	name, _, _ := p.DetectProvider(req)
	if name != "OpenRouter" {
		t.Fatalf("provider label = %q, want %q", name, "OpenRouter")
	}
}

// TestSortedProviderKeys_TotalOrder pins the ordering contract itself: longest
// first so the most specific alias wins, alphabetical within a length so the
// order is total (never dependent on map layout), and "default" excluded.
func TestSortedProviderKeys_TotalOrder(t *testing.T) {
	got := sortedProviderKeys(map[string]string{
		"bb":      "x",
		"aa":      "x",
		"cccc":    "x",
		"default": "x",
	})
	want := []string{"cccc", "aa", "bb"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
