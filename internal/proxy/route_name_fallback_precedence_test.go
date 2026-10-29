package proxy

import "testing"

// A route matched only by its Name must never beat another route whose
// configured PathPrefix matches the request. Routes are sorted by prefix
// length, so a longer, unrelated prefix used to be tried first and won through
// its name, forwarding the caller's Authorization header to its upstream.
func TestMatchRoute_ConfiguredPrefixBeatsNameFallback(t *testing.T) {
	rm := &RouteManager{routes: make(map[string]ProxyRoute)} // no disk state
	rm.routes["by-name"] = ProxyRoute{
		ID: "by-name", Name: "zai", PathPrefix: "/v1/zai-coding-plan",
		TargetUpstream: "https://other.example", ProtocolType: "openai-compatible", Enabled: true,
	}
	rm.routes["by-prefix"] = ProxyRoute{
		ID: "by-prefix", Name: "intended", PathPrefix: "/proxy/zai",
		TargetUpstream: "https://intended.example", ProtocolType: "openai-compatible", Enabled: true,
	}

	for path, wantRem := range map[string]string{"/proxy/zai": "/", "/proxy/zai/v1/chat": "/v1/chat"} {
		r, rem := rm.MatchRoute(path)
		if r == nil || r.ID != "by-prefix" || rem != wantRem {
			id := "<nil>"
			if r != nil {
				id = r.ID
			}
			t.Errorf("MatchRoute(%q) = %s %q; want by-prefix %q", path, id, rem, wantRem)
		}
	}

	// Control: with no prefix match, the name fallback still routes.
	delete(rm.routes, "by-prefix")
	if r, rem := rm.MatchRoute("/proxy/zai/v1/chat"); r == nil || r.ID != "by-name" || rem != "/v1/chat" {
		t.Errorf("name fallback lost: %+v %q", r, rem)
	}
}
