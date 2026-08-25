package server

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

// freePort reserves an ephemeral port so the pprof listener has somewhere
// deterministic to bind in tests.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer l.Close()
	return l.Addr().String()
}

// Without WRONGTRACE_PPROF set, StartDebugServer must be a complete no-op:
// no goroutine, no listener. Verified by binding a listener ourselves and
// confirming the address stays free afterwards.
func TestStartDebugServer_DisabledByDefault(t *testing.T) {
	t.Setenv("WRONGTRACE_PPROF", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addr := freePort(t)
	t.Setenv("WRONGTRACE_PPROF_ADDR", addr)

	StartDebugServer(ctx)

	// Give an (incorrectly started) listener a moment to surface, then prove
	// the port is still unused.
	time.Sleep(50 * time.Millisecond)
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("port %s occupied but pprof should be disabled: %v", addr, err)
	}
	_ = l.Close()
}

func TestStartDebugServer_EnabledServesPprof(t *testing.T) {
	t.Setenv("WRONGTRACE_PPROF", "1")
	addr := freePort(t)
	t.Setenv("WRONGTRACE_PPROF_ADDR", addr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	StartDebugServer(ctx)

	base := "http://" + addr
	client := &http.Client{Timeout: 2 * time.Second}

	// The listener starts asynchronously; poll for readiness.
	var resp *http.Response
	deadline := time.Now().Add(3 * time.Second)
	for {
		var err error
		resp, err = client.Get(base + "/debug/pprof/")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pprof index never became ready: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /debug/pprof/: status = %d, want 200", resp.StatusCode)
	}

	// cmdline is the cheapest real handler; /debug/pprof/ is index HTML.
	resp2, err := client.Get(base + "/debug/pprof/cmdline")
	if err != nil {
		t.Fatalf("GET cmdline: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("GET /debug/pprof/cmdline: status = %d, want 200", resp2.StatusCode)
	}

	// Shutdown via ctx must stop the listener: once the pprof server's
	// listener is closed we can bind the port ourselves. Bind SUCCESS means
	// the port is free; bind FAILURE means it is still occupied.
	cancel()
	deadline = time.Now().Add(3 * time.Second)
	for {
		l, err := net.Listen("tcp", addr)
		if err != nil {
			if time.Now().After(deadline) {
				t.Fatalf("listener still bound after ctx cancel: %v", err)
			}
			time.Sleep(25 * time.Millisecond)
			continue
		}
		_ = l.Close() // bound fine — port released; done.
		return
	}
}
