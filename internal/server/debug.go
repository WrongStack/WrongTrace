package server

import (
	"context"
	"log"
	"net/http"
	"net/http/pprof"
	"os"
	"strings"
	"time"
)

// StartDebugServer starts an optional pprof listener for CPU/heap profiling.
//
// The daemon has no profiling surface on the main API port by design — profile
// endpoints expose internal state and have no place on an unauthenticated
// router. Instead, opt in via environment:
//
//	WRONGTRACE_PPROF=1             # listen on 127.0.0.1:6060
//	WRONGTRACE_PPROF=1 WRONGTRACE_PPROF_ADDR=:6061
//
// When WRONGTRACE_PPROF is unset, this is a no-op. The listener binds
// loopback by default; an explicit WRONGTRACE_PPROF_ADDR can widen it for
// container/remote setups where loopback is unreachable.
//
// Run `go tool pprof http://127.0.0.1:6060/debug/pprof/profile?seconds=30`
// for a 30s CPU profile.
func StartDebugServer(ctx context.Context) {
	if !strings.EqualFold(strings.TrimSpace(os.Getenv("WRONGTRACE_PPROF")), "1") {
		return
	}
	addr := strings.TrimSpace(os.Getenv("WRONGTRACE_PPROF_ADDR"))
	if addr == "" {
		addr = "127.0.0.1:6060"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	go func() {
		hs := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = hs.Shutdown(shutdownCtx)
		}()
		log.Printf("pprof: listening on http://%s/debug/pprof/ (WRONGTRACE_PPROF=1)", addr)
		if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("pprof: %v", err)
		}
	}()
}
