package watcher

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// gatedHandler blocks its first HandleFileChange until gate is closed.
type gatedHandler struct {
	entered  chan struct{}
	gate     chan struct{}
	inFlight atomic.Int32
	fired    atomic.Bool
}

func (h *gatedHandler) HandleFileChange(ctx context.Context, path string) {
	if !h.fired.CompareAndSwap(false, true) {
		return
	}
	h.inFlight.Add(1)
	defer h.inFlight.Add(-1)
	close(h.entered)
	<-h.gate
}

// Once Run returns its caller closes the engine, store and parser, so no
// HandleFileChange Run started may still be executing. The handler is
// released only after the stop signal, so Run has to wait for it.
func TestRun_WaitsForInFlightHandlers(t *testing.T) {
	for _, via := range []string{"cancel", "close"} {
		t.Run(via, func(t *testing.T) {
			dir := t.TempDir()
			h := &gatedHandler{entered: make(chan struct{}), gate: make(chan struct{})}
			w, err := New(Config{Dir: dir, Engine: h, Debounce: time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runDone := make(chan struct{})
			go func() { w.Run(ctx); close(runDone) }()

			if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			select {
			case <-h.entered:
			case <-time.After(20 * time.Second): // failure guard only
				t.Fatal("handler never invoked")
			}
			if via == "cancel" {
				cancel()
			} else {
				w.Close()
			}
			go close(h.gate)
			<-runDone
			if n := h.inFlight.Load(); n != 0 {
				t.Fatalf("Run returned with %d HandleFileChange call(s) still executing", n)
			}
			if via == "cancel" {
				w.Close()
			}
		})
	}
}
