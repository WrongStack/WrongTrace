package proxy

import (
	"fmt"
	"os"
	"testing"
)

// TestMain isolates WRONGTRACE_HOME for the whole package. RouteManager loads
// and saves proxy_routes.json under that directory, so without isolation every
// UpsertRoute in a test appended a route to the developer's real
// ~/.wrongtrace/proxy_routes.json — and a previously saved route with the same
// prefix then decided which route the test matched, so TestDetectProvider
// passed on a dirty workstation and failed on a clean CI runner.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "wrongtrace-proxy-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "proxy TestMain: temp home:", err)
		os.Exit(1)
	}
	if err := os.Setenv("WRONGTRACE_HOME", home); err != nil {
		fmt.Fprintln(os.Stderr, "proxy TestMain: setenv:", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
