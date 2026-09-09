package proxy

import "testing"

// TestMatchRoute_SlugDerivationIsBoundaryCorrect proves that MatchRoute case 2
// (flexible /proxy/<slug> match) does not hijack /proxy/zai traffic when a route
// with PathPrefix="/proxyzai" (prefix collision) is registered.
//
// BUG: Without the boundary guard (lowerPfx == "/proxy" || HasPrefix(lowerPfx, "/proxy/")),
// strings.TrimPrefix(lowerPfx, "/proxy") strips the prefix from any path that starts with
// "/proxy", even mid-segment. A route with PathPrefix="/proxyzai" derives proxySlug="/proxy/zai"
// (slug="zai") and incorrectly matches /proxy/zai requests, forwarding Authorization headers
// to the wrong upstream host.
//
// FIX: Guard the strip with a path-segment boundary check so /proxyzai is only considered
// when the incoming request path is exactly "/proxyzai" or starts with "/proxyzai/".
func TestMatchRoute_SlugDerivationIsBoundaryCorrect(t *testing.T) {
	rm := NewRouteManager()

	// Route A: /proxyzai — starts with /proxy, but is NOT /proxy/zai.
	// Without the fix, case 2 derives proxySlug="/proxy/zai" and hijacks /proxy/zai traffic.
	r1 := ProxyRoute{
		ID:             "route-proxyzai",
		Name:           "proxyzai",
		PathPrefix:     "/proxyzai",
		TargetUpstream: "https://evil.example.com",
		ProtocolType:   "openai-compatible",
		Enabled:        true,
	}
	rm.routes["route-proxyzai"] = r1

	// Route B: /proxy/zai — the legitimate route.
	r2 := ProxyRoute{
		ID:             "route-zai",
		Name:           "zai",
		PathPrefix:     "/proxy/zai",
		TargetUpstream: "https://api.z.ai",
		ProtocolType:   "openai-compatible",
		Enabled:        true,
	}
	rm.routes["route-zai"] = r2

	// --- /proxyzai: must match Route A (exact) via case 1 ---
	r, rem := rm.MatchRoute("/proxyzai")
	if r == nil {
		t.Fatal("FAIL: /proxyzai matched no route")
	}
	if r.PathPrefix != "/proxyzai" {
		t.Fatalf("FAIL: /proxyzai matched prefix=%q, want /proxyzai. Route hijacked!", r.PathPrefix)
	}
	if rem != "/" {
		t.Fatalf("FAIL: /proxyzai remaining=%q, want \"/\"", rem)
	}

	// --- /proxyzai/chat: must match Route A (exact prefix) via case 1 ---
	r, rem = rm.MatchRoute("/proxyzai/chat")
	if r == nil {
		t.Fatal("FAIL: /proxyzai/chat matched no route")
	}
	if r.PathPrefix != "/proxyzai" {
		t.Fatalf("FAIL: /proxyzai/chat matched prefix=%q, want /proxyzai. Route hijacked!", r.PathPrefix)
	}
	if rem != "/chat" {
		t.Fatalf("FAIL: /proxyzai/chat remaining=%q, want \"/chat\"", rem)
	}

	// --- /proxy/zai: must match Route B (exact) via case 1 ---
	r, rem = rm.MatchRoute("/proxy/zai")
	if r == nil {
		t.Fatal("FAIL: /proxy/zai matched no route")
	}
	if r.PathPrefix != "/proxy/zai" {
		t.Fatalf("FAIL: /proxy/zai matched prefix=%q, want /proxy/zai. Route hijacked by %s!", r.PathPrefix, r.TargetUpstream)
	}
	if rem != "/" {
		t.Fatalf("FAIL: /proxy/zai remaining=%q, want \"/\"", rem)
	}

	// --- /proxy/zai/chat/completions: must match Route B (prefix) via case 1 ---
	r, rem = rm.MatchRoute("/proxy/zai/chat/completions")
	if r == nil {
		t.Fatal("FAIL: /proxy/zai/chat/completions matched no route")
	}
	if r.PathPrefix != "/proxy/zai" {
		t.Fatalf("FAIL: /proxy/zai/chat/completions matched prefix=%q, want /proxy/zai. Route hijacked by %s!", r.PathPrefix, r.TargetUpstream)
	}
	if rem != "/chat/completions" {
		t.Fatalf("FAIL: /proxy/zai/chat/completions remaining=%q, want \"/chat/completions\"", rem)
	}

	// --- /proxyzaii (one extra 'i'): must NOT match either route ---
	r, rem = rm.MatchRoute("/proxyzaii")
	if r != nil {
		t.Fatalf("FAIL: /proxyzaii unexpectedly matched prefix=%q, want nil", r.PathPrefix)
	}
	// A rejected path must also come back UNCHANGED. MatchRoute's no-match
	// fallback is `return nil, path`, i.e. the caller's original string; the
	// /proxyzai, /proxyzai/chat and /proxy/zai cases above all assert their
	// remaining path, and this one silently dropped its value (staticcheck
	// SA4006) even though it is the case that proves the boundary guard neither
	// matches nor hands back a stripped remainder.
	if rem != "/proxyzaii" {
		t.Fatalf("FAIL: /proxyzaii remaining=%q, want %q echoed back verbatim", rem, "/proxyzaii")
	}
}
