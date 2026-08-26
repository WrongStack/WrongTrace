package main

import (
	"log"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"time"
)

// WrongTrace is a background observer: it must never be the reason a machine
// feels slow. Two runtime knobs bound that far more effectively than any
// micro-optimization in the hot paths.

const (
	// defaultMemoryLimitBytes is the soft ceiling the GC paces against.
	defaultMemoryLimitBytes = 512 << 20
	// defaultGCPercent trades a little more GC CPU for a much smaller heap.
	// At the default 100 the heap grows to twice the live set before a cycle
	// runs; 50 halves that headroom, which is the single biggest lever on
	// resident size for a long-lived process with a large live set.
	defaultGCPercent = 50
	// scavengeInterval is how often idle heap spans are offered back to the OS.
	scavengeInterval = 5 * time.Minute
	// scavengeThresholdBytes is the amount of unreturned idle heap that makes a
	// forced release worth its stop-the-world pause.
	scavengeThresholdBytes = 32 << 20
)

// tuneRuntime applies the GC and parallelism limits. Every value is
// overridable so a batch or CI run can opt out of throttling.
func tuneRuntime() {
	limit := int64(defaultMemoryLimitBytes)
	if mb := envInt("WRONGTRACE_MEMORY_LIMIT_MB", 0); mb > 0 {
		limit = int64(mb) << 20
	}
	debug.SetMemoryLimit(limit)

	gcPercent := envInt("WRONGTRACE_GC_PERCENT", defaultGCPercent)
	if gcPercent > 0 {
		debug.SetGCPercent(gcPercent)
	}

	// GOMAXPROCS defaults to every core, which lets a single parse burst or GC
	// cycle briefly claim the whole machine. The daemon's work is either
	// I/O-bound (proxy, database) or deliberately paced (indexing), so it has
	// nothing to gain from more than a few threads -- and capping it also caps
	// the GC's dedicated workers, which are sized off GOMAXPROCS.
	if procs := envInt("WRONGTRACE_MAX_PROCS", 0); procs > 0 {
		runtime.GOMAXPROCS(procs)
	} else if n := runtime.NumCPU(); n > 4 {
		runtime.GOMAXPROCS(4)
	}

	log.Printf("runtime: memlimit=%dMiB gogc=%d gomaxprocs=%d", limit>>20, gcPercent, runtime.GOMAXPROCS(0))
}

// startScavenger periodically returns idle heap to the operating system.
//
// Go's background scavenger releases memory only gradually, so after a burst
// (indexing a workspace, a large streamed LLM response) the process can hold
// its peak RSS for a long time even though the heap is nearly empty. That is
// what users see as "it eats RAM and never gives it back". The forced release
// costs one stop-the-world pause, so it is rate-limited and skipped entirely
// unless there is a meaningful amount to reclaim.
func startScavenger(done <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(scavengeInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				if m.HeapIdle-m.HeapReleased < scavengeThresholdBytes {
					continue
				}
				debug.FreeOSMemory()
			}
		}
	}()
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return fallback
}
