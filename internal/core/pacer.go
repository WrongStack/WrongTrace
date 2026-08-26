package core

import (
	"os"
	"runtime/debug"
	"strconv"
	"time"
)

// PrimeDirectory reads and Tree-sitter-parses every eligible file in a
// workspace. On a cold start over a large repository that is a sustained,
// single-threaded burst of file I/O and CPU that lands exactly when the user
// has just opened their editor -- the one moment the daemon must be invisible.
//
// runtime.Gosched() was the previous mitigation. It only offers the scheduler a
// switch; with a free core available the goroutine is rescheduled immediately
// and the indexer still pins that core to 100%. A pacer that actually sleeps is
// the only way to bound the share.

// defaultIndexCPUPercent is the share of ONE core the indexer may use. Half
// leaves an interactive machine responsive while still indexing a large
// workspace in a reasonable time.
const defaultIndexCPUPercent = 50

// indexCPUPercent is overridable through WRONGTRACE_INDEX_CPU (1-100). 100
// disables pacing for batch/CI runs where latency does not matter.
var indexCPUPercent = func() int {
	if v := os.Getenv("WRONGTRACE_INDEX_CPU"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 100 {
			return n
		}
	}
	return defaultIndexCPUPercent
}()

// pacerSlice is how much work accumulates before the pacer sleeps. Small
// enough that the duty cycle is smooth, large enough that sleep overhead and
// timer granularity (~1ms on Windows) stay negligible.
const pacerSlice = 20 * time.Millisecond

// indexPacer keeps a walk under a fixed share of one core by sleeping in
// proportion to the CPU time it just consumed.
type indexPacer struct {
	percent int
	worked  time.Duration
	last    time.Time
}

func newIndexPacer() *indexPacer {
	return &indexPacer{percent: indexCPUPercent, last: time.Now()}
}

// step accounts for the work done since the previous call and sleeps when the
// accumulated slice would exceed the configured duty cycle. Call it once per
// indexed file.
func (p *indexPacer) step() {
	if p == nil || p.percent >= 100 {
		return
	}
	now := time.Now()
	p.worked += now.Sub(p.last)
	p.last = now
	if p.worked < pacerSlice {
		return
	}
	// Working w at d% duty means resting w*(100-d)/d.
	rest := time.Duration(int64(p.worked) * int64(100-p.percent) / int64(p.percent))
	p.worked = 0
	time.Sleep(rest)
	p.last = time.Now()
}

// releaseIndexMemory hands the indexing spike back to the OS. Parsing a
// workspace allocates far more than the resulting snapshot cache retains, and
// Go's scavenger returns that arena only gradually -- on Windows the process
// can sit at its peak RSS for minutes afterwards, which is precisely the
// "it eats RAM" symptom. A single forced release at the end of a bounded,
// infrequent job is worth the one stop-the-world pause it costs.
func releaseIndexMemory() {
	debug.FreeOSMemory()
}
