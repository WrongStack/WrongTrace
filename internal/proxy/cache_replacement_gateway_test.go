package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// End-to-end reproduction of the response-cache replacement-eviction defect
// through the REAL gateway: NewGatewayProxy -> httptest listener -> httptest
// upstream -> provider routing -> cache-key derivation (ComputeRequestKey +
// requestCacheScope) -> upstream fetch -> async finalize pipeline ->
// ResponseCache store.
//
// It exists to replace a source-structural reachability argument ("one Get
// site, one store site, no singleflight") with an executed demonstration, and
// to measure the harm the way an operator would: an extra paid upstream call
// for a response that was already cached.
//
// Harness invariants that make a green run mean something:
//
//   - The cache is swapped for a small-capacity *real* ResponseCache built by
//     the production constructor. GatewayProxy reads p.Cache per request at
//     both the lookup and the store site, so capacity is the only variable
//     changed; no production code is modified. Production wires
//     NewResponseCache(128, 24h) at proxy.go:192, and filling 128 slots to
//     reach the eviction boundary would only re-run the same mechanism at
//     32x the cost.
//
//   - The cache fill runs OFF the request path on the single finalize worker
//     (enqueueFinalize -> finalizeWorker -> finalize -> SetWithReplayHeader),
//     so waitFinalize() is the join point. enqueueFinalize DROPS jobs on a
//     full queue, which would silently skip a store and could mask the defect,
//     so finalizeDropped is asserted to be zero.
//
//   - The upstream gate is armed only for the burst window (the sequential
//     victim-seed requests must not wait on it, or they deadlock), holds every
//     burst response until all burst requests have arrived, and always
//     unblocks on cleanup so a failing assertion cannot wedge httptest
//     Server.Close. Because a store can only happen after its upstream
//     responds, that barrier makes "every duplicate observed a cache miss" a
//     measured fact rather than a timing hope.
//
//   - Victim liveness is NEVER probed with a cache read mid-workload: Get()
//     refreshes lastAccessUnix and would move the next eviction victim. The
//     accrual measurement therefore reads occupancy only, through Stats(),
//     which takes no recency stamps.

// gatedUpstream is an httptest upstream that counts calls and, while armed,
// parks every response until `want` calls have arrived and the test releases.
// It is re-armable so a multi-round workload can gate each round separately.
type gatedUpstream struct {
	srv *httptest.Server

	armed atomic.Bool
	calls atomic.Int64

	mu         sync.Mutex
	want       int32
	arrivals   int
	gate       chan struct{} // closed when the armed window fills
	release    chan struct{} // closed by the test to let responses through
	gateClosed bool
	released   bool

	// censusMode makes the upstream answer 200 with an EMPTY body. The gateway's
	// store guard is `len(job.respBytes) > 0`, so a cache miss in this mode
	// performs no store, hence no eviction: the census can then probe every key
	// for residence without a dead probe evicting the next cold survivor.
	censusMode atomic.Bool
}

func newGatedUpstream(t *testing.T) *gatedUpstream {
	t.Helper()
	g := &gatedUpstream{gate: make(chan struct{}), release: make(chan struct{})}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.calls.Add(1)
		if g.censusMode.Load() {
			// 200 with an empty body: the gateway's store guard
			// `len(job.respBytes) > 0` rejects it, so a cache miss here cannot
			// store, and therefore cannot evict. Without this, probing a dead
			// cold key would insert a fresh one and evict the next, oldest,
			// still-live cold key — making the census destroy the very victims
			// it is supposed to count.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			return
		}
		if g.armed.Load() {
			g.mu.Lock()
			g.arrivals++
			if !g.gateClosed && g.arrivals == int(g.want) {
				g.gateClosed = true
				close(g.gate)
			}
			gate, release := g.gate, g.release
			g.mu.Unlock()
			select {
			case <-gate:
			case <-time.After(5 * time.Second):
			}
			select {
			case <-release:
			case <-time.After(5 * time.Second):
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id": "cmpl-gw-repro",
			"choices": []map[string]interface{}{{
				"message":       map[string]string{"role": "assistant", "content": "gateway repro"},
				"finish_reason": "stop",
			}},
			"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5},
		})
	}))
	return g
}

// arm opens the gate for exactly want concurrent upstream calls. Callers must
// release the previous window before re-arming, or a still-parked handler waits
// out its timeout; the tests below join on awaitGate+releaseNow before firing
// the next round, so that cannot happen.
func (g *gatedUpstream) arm(want int) {
	g.mu.Lock()
	g.arrivals = 0
	g.gateClosed = false
	g.released = false
	g.gate = make(chan struct{})
	g.release = make(chan struct{})
	g.want = int32(want)
	g.mu.Unlock()
	g.armed.Store(true)
}

func (g *gatedUpstream) disarm() { g.armed.Store(false) }

// awaitGate blocks until `want` burst requests have arrived upstream, so the
// caller can release them without waiting on responses that cannot complete
// until they are released. Reports false if the window never filled.
func (g *gatedUpstream) awaitGate() bool {
	g.mu.Lock()
	gate := g.gate
	g.mu.Unlock()
	select {
	case <-gate:
		return true
	case <-time.After(5 * time.Second):
		return false
	}
}

// releaseNow lets the armed window's parked responses through. Idempotent per
// window; re-arm resets it for the next round.
func (g *gatedUpstream) releaseNow() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.released {
		return
	}
	g.released = true
	close(g.release)
}

func (g *gatedUpstream) arrivedCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.arrivals
}

// completionBody builds a distinct request body, hence a distinct cache key.
func completionBody(text string) string {
	return fmt.Sprintf(`{"model":"gpt-4o","messages":[{"role":"user","content":%q}]}`, text)
}

// gatewayProbe issues one cache-enabled request through the gateway and
// reports whether the gateway served it from cache. It returns an error
// instead of calling t.Fatalf so it is safe to use from worker goroutines.
func gatewayProbe(url, body string) (fromCache bool, err error) {
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return false, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-WrongTrace-Cache", "allow")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("gateway status = %d, want 200", resp.StatusCode)
	}
	return strings.EqualFold(resp.Header.Get("X-WrongTrace-Cache"), "HIT"), nil
}

func mustProbe(t *testing.T, url, body string) bool {
	t.Helper()
	hit, err := gatewayProbe(url, body)
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}
	return hit
}

// newGatewayWithCapacity builds the real gateway, then swaps in a real
// ResponseCache of the requested capacity. Cleanup order is arranged by the
// callers so the gate is released before any server is closed.
func newGatewayWithCapacity(t *testing.T, upstreamURL string, capacity int) (*GatewayProxy, *httptest.Server) {
	t.Helper()
	proxy := NewGatewayProxy(Config{CustomUpstreams: map[string]string{"cached_mock": upstreamURL}})
	t.Cleanup(proxy.Close) // runs last: after the listener is shut down
	proxy.Cache = NewResponseCache(capacity, time.Hour)
	gw := httptest.NewServer(proxy)
	t.Cleanup(gw.Close)
	return proxy, gw
}

// TestGateway_DuplicateConcurrentRequestStoresDoNotEvictUnrelatedEntry is the
// end-to-end statement of the defect.
//
// Layout (capacity 4): victims V1,V2,V3 are cached first and never read again,
// so V1 is the least-recently-used resident. Four byte-identical requests for a
// NEW prompt then run concurrently. The first store is a legitimate insertion;
// the other three are replacements of an already-resident key. Pre-fix, the
// second store ran the eviction pass at capacity and picked V1 — not the
// replaced key, whose stamp is the newest — and deleted it. Post-fix, a store
// that does not grow the map makes no room at all.
func TestGateway_DuplicateConcurrentRequestStoresDoNotEvictUnrelatedEntry(t *testing.T) {
	const (
		capacity  = 4
		burstSize = 4
	)

	victimBodies := map[string]string{
		"V1": completionBody("victim one"),
		"V2": completionBody("victim two"),
		"V3": completionBody("victim three"),
	}

	up := newGatedUpstream(t)
	proxy, gw := newGatewayWithCapacity(t, up.srv.URL, capacity)
	// Registered last so cleanup unwinds as: release -> upstream -> listener -> proxy.
	t.Cleanup(up.srv.Close)
	t.Cleanup(up.releaseNow)

	target := gw.URL + "/proxy/cached_mock/v1/chat/completions"

	// --- Phase 1: seed three victims sequentially. Each is a genuine
	// insertion below capacity and is never read again, so their stamps stay
	// the lowest in the cache.
	for _, name := range []string{"V1", "V2", "V3"} {
		if mustProbe(t, target, victimBodies[name]) {
			t.Fatalf("harness: victim %s unexpectedly served from cache on first use", name)
		}
	}
	proxy.waitFinalize()
	if got := up.calls.Load(); got != 3 {
		t.Fatalf("harness: upstream calls = %d, want 3 after seeding victims", got)
	}
	if entries, _, _, _, _ := proxy.Cache.Stats(); entries != capacity-1 {
		t.Fatalf("harness: occupancy = %d, want %d after seeding", entries, capacity-1)
	}

	// --- Phase 2: fire burstSize byte-identical requests concurrently, with
	// the upstream parked until all of them are in flight.
	up.arm(burstSize)
	var wg sync.WaitGroup
	hits := make([]bool, burstSize)
	errs := make([]error, burstSize)
	for i := 0; i < burstSize; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			hits[i], errs[i] = gatewayProbe(target, completionBody("duplicate burst prompt"))
		}(i)
	}
	// Join on ARRIVALS, not on responses: the responses cannot complete until
	// the gate is released, so releasing after wg.Wait() would self-deadlock.
	gateFilled := up.awaitGate()
	up.releaseNow()
	wg.Wait()
	up.disarm()
	proxy.waitFinalize()

	if !gateFilled {
		t.Fatalf("harness: burst gate never filled (%d of %d duplicates arrived)", up.arrivedCount(), burstSize)
	}

	for i, err := range errs {
		if err != nil {
			t.Fatalf("burst request %d failed: %v", i, err)
		}
	}

	// Claim A — the premise itself: there is no in-flight dedup, so every
	// duplicate paid for its own upstream round trip and none was served from
	// cache. If the barrier had leaked, arrivals would be short of burstSize.
	if got := up.arrivedCount(); got != burstSize {
		t.Fatalf("harness: only %d of %d duplicates reached upstream; misses were not simultaneous", got, burstSize)
	}
	wantCalls := int64(3 + burstSize)
	if got := up.calls.Load(); got != wantCalls {
		t.Errorf("upstream calls = %d, want %d; the duplicates did not all miss", got, wantCalls)
	}
	for i, h := range hits {
		if h {
			t.Errorf("duplicate request %d was served from cache; barrier failed to force simultaneous misses", i)
		}
	}

	// Harness integrity: a dropped finalize job would skip a store and could
	// hide the eviction, so a green run must prove none were dropped.
	if dropped := proxy.finalizeDropped.Load(); dropped != 0 {
		t.Fatalf("harness: %d finalize job(s) dropped; stores not guaranteed", dropped)
	}

	// Claim B — all burstSize stores collapsed onto ONE key (byte-identical
	// request => byte-identical cache key), so burstSize-1 of them were
	// replacements. A replacement must not shrink the cache.
	entries, _, _, _, _ := proxy.Cache.Stats()
	if entries != capacity {
		t.Errorf("cache occupancy = %d, want %d: a redundant replacement of an already-resident key evicted an unrelated live response",
			entries, capacity)
	}

	// Claim C — the operator-visible harm. The coldest victim V1 is still
	// live, so a byte-identical repeat must be served from cache without
	// touching the upstream. Pre-fix, the burst destroyed V1 and this repeat
	// became a second paid upstream call.
	callsBefore := up.calls.Load()
	if !mustProbe(t, target, victimBodies["V1"]) {
		t.Errorf("victim V1 was evicted by a redundant replacement of an already-resident key; a response that was cached is now re-fetched upstream")
	}
	proxy.waitFinalize()
	if got := up.calls.Load(); got != callsBefore {
		t.Errorf("upstream calls went %d -> %d on a repeat of V1; a cache HIT must not reach the upstream (delta %d = one wasted paid LLM call)",
			callsBefore, got, got-callsBefore)
	}

	// Claim D — the replacement itself landed and is readable.
	if !mustProbe(t, target, completionBody("duplicate burst prompt")) {
		t.Error("the duplicate-burst response is not served from cache; the store did not land")
	}
	proxy.waitFinalize()
}

// TestGateway_SingleRedundantStoreIsTheMinimalReproduction pins the smallest
// end-to-end case: capacity 3, two live victims, exactly TWO concurrent
// identical requests. A single redundant store is enough to destroy one
// unrelated live response, so the defect does not depend on a large burst.
func TestGateway_SingleRedundantStoreIsTheMinimalReproduction(t *testing.T) {
	const (
		capacity  = 3
		burstSize = 2
	)

	up := newGatedUpstream(t)
	proxy, gw := newGatewayWithCapacity(t, up.srv.URL, capacity)
	t.Cleanup(up.srv.Close)
	t.Cleanup(up.releaseNow)

	target := gw.URL + "/proxy/cached_mock/v1/chat/completions"

	for _, name := range []string{"alpha", "beta"} {
		if mustProbe(t, target, completionBody("victim-"+name)) {
			t.Fatalf("harness: victim %s unexpectedly cached on first use", name)
		}
	}
	proxy.waitFinalize()

	up.arm(burstSize)
	var wg sync.WaitGroup
	for i := 0; i < burstSize; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = gatewayProbe(target, completionBody("shared-prompt"))
		}()
	}
	gateFilled := up.awaitGate()
	up.releaseNow()
	wg.Wait()
	up.disarm()
	proxy.waitFinalize()

	if !gateFilled {
		t.Fatalf("harness: burst gate never filled (%d of %d duplicates arrived)", up.arrivedCount(), burstSize)
	}
	if dropped := proxy.finalizeDropped.Load(); dropped != 0 {
		t.Fatalf("harness: %d finalize job(s) dropped", dropped)
	}
	if got := up.calls.Load(); got != 4 {
		t.Errorf("upstream calls = %d, want 4 (2 victims + 2 simultaneous misses)", got)
	}
	if entries, _, _, _, _ := proxy.Cache.Stats(); entries != capacity {
		t.Errorf("cache occupancy = %d, want %d after a single redundant store", entries, capacity)
	}
	if !mustProbe(t, target, completionBody("victim-alpha")) {
		t.Error("victim alpha evicted by one redundant replacement")
	}
	proxy.waitFinalize()
}

// TestGateway_InterleavedTrafficLossSaturatesAtOnePermanentSlot measures the
// SHAPE of the damage, which is what decides how severe this bug is in
// production, and it is the measurement that settled a claim I had wrong twice.
//
// Within one burst the eviction self-disarms: after the first replacement
// destroys an entry, len(items) drops below capacity, so the remaining N-1
// replacements fail the `>= maxEntries` test. The open question was whether
// INTERLEAVED traffic re-arms it often enough that the loss accrues per round
// (a rate) or stays bounded (a constant).
//
// Measured so far: BOUNDED. The per-round occupancy vector is flat after the
// first round, and the shortfall was exactly one slot across 5 and 10 rounds
// and across duplicate fan-out 2/3/5. The reason is backfill: once the defect
// punches a hole, the next round's legitimate insertion fills that hole instead
// of evicting, so it destroys nothing, and the round's replacement eviction then
// costs one again. One legitimate eviction is absorbed per round, so the
// population parks at capacity-1.
//
// Workload per cell (I = legitimate fresh-key insertions per round, R =
// redundant replacement stores per round): I-1 sequential distinct inserts,
// then one concurrent burst of R+1 byte-identical requests. A burst is the only
// way to obtain a replacement store, because a live resident key returns a HIT
// and never stores, and it necessarily contributes one insertion plus R
// replacements. The matrix sweeps I in {1,2,3} against R in {1,2,4} to test
// whether the saturating constant can be pushed above one by the insertion rate
// as well as by fan-out. This sweep is already covered for the I=1 row (R+1 =
// burst size), which is the cross-check the matrix is anchored to.
//
// Capacity is swept as a third axis, {8, 128}: 8 is the size every earlier
// hand-trace used, 128 is the production wiring (proxy.go:192
// NewResponseCache(128, 24h)). It matters because it decides which census can
// see the damage. Destruction is rounds*inserts + 1 (unfixed) against
// rounds*inserts (fixed), so the excess is one eviction whatever the capacity;
// but at capacity 8 with 10 rounds that budget (11/21/31) exceeds the 8-body
// cold set, so colds saturate and only the round-key census discriminates,
// while at capacity 128 the same budget is absorbed entirely by colds, so the
// cold census discriminates and the round-key census reads zero. A conclusion
// that holds at both capacities is therefore not an artifact of the smaller one.
//
// Measurement is occupancy-only, through Stats(), which takes no recency
// stamps. Probing a specific victim's survival would require a cache read, and
// Get() refreshes lastAccessUnix, moving the next eviction victim and
// corrupting the trajectory this test exists to observe.
func TestGateway_InterleavedTrafficLossSaturatesAtOnePermanentSlot(t *testing.T) {
	const rounds = 10

	// Capacity 8 is the size every earlier hand-trace used; 128 is what
	// production actually wires (proxy.go:192). Sweeping both tests whether the
	// damage constant is a property of the defect or an artifact of the smaller
	// fixture.
	capacities := []int{8, 128}

	// Two independent mix axes, swept as a matrix:
	//
	//   inserts      legitimate fresh-key insertions per round
	//   replacements redundant replacement stores of an already-resident key
	//                per round
	//
	// A replacement store can only be produced by a concurrent duplicate burst
	// (a live resident key returns a HIT and never stores), and such a burst of
	// R+1 byte-identical requests necessarily contributes exactly one insertion
	// plus R replacements. So a cell (I, R) is issued as I-1 sequential distinct
	// inserts followed by a burst of R+1 duplicates. The I=1 row therefore
	// reproduces the earlier burstSize 2/3/5 sweep (R+1 = burst size), which is
	// the cross-check this matrix is anchored to.
	type mix struct{ capacity, inserts, replacements int }
	var mixes []mix
	for _, capacity := range capacities {
		for _, ir := range [][2]int{
			{1, 1}, {1, 2}, {1, 4},
			{2, 1}, {2, 2}, {2, 4},
			{3, 1}, {3, 2}, {3, 4},
		} {
			mixes = append(mixes, mix{capacity, ir[0], ir[1]})
		}
	}

	type cellResult struct {
		m                 mix
		permanentSlotLoss int
		peakSlotsLost     int
		occupancy         []int
		calls             int64
		// Destruction census: counts every live response destroyed, including
		// ones whose slot was later backfilled and which are therefore invisible
		// to the occupancy shortfall.
		coldsDestroyed  int
		roundsDestroyed int
	}
	var results []cellResult

	for _, mx := range mixes {
		mx := mx
		t.Run(fmt.Sprintf("cap%d_ins%d_rep%d", mx.capacity, mx.inserts, mx.replacements), func(t *testing.T) {
			// Bound per cell so the whole body (fill loop, Stats assertions,
			// shortfall arithmetic, census) runs unchanged at either capacity.
			capacity := mx.capacity

			up := newGatedUpstream(t)
			proxy, gw := newGatewayWithCapacity(t, up.srv.URL, capacity)
			// Registered last so cleanup unwinds: release -> upstream -> listener -> proxy.
			t.Cleanup(up.srv.Close)
			t.Cleanup(up.releaseNow)

			target := gw.URL + "/proxy/cached_mock/v1/chat/completions"

			// Fill to capacity with cold entries: the bodies the defect destroys,
			// none of which is ever read again.
			for i := 0; i < capacity; i++ {
				if mustProbe(t, target, completionBody(fmt.Sprintf("cold-%d", i))) {
					t.Fatalf("harness: cold entry %d unexpectedly cached on first use", i)
				}
			}
			proxy.waitFinalize()
			startEntries, _, _, _, _ := proxy.Cache.Stats()
			if startEntries != capacity {
				t.Fatalf("harness: occupancy = %d, want %d before the rounds", startEntries, capacity)
			}

			seq := mx.inserts - 1
			burst := mx.replacements + 1
			occupancyByRound := make([]int, 0, rounds)

			for r := 0; r < rounds; r++ {
				// Legitimate insertions first, awaited, so the round's ordering is
				// deterministic instead of racing the single finalize worker.
				for k := 0; k < seq; k++ {
					if mustProbe(t, target, completionBody(fmt.Sprintf("r%d-ins%d", r, k))) {
						t.Fatalf("harness: round %d insert %d unexpectedly served from cache", r, k)
					}
				}
				proxy.waitFinalize()

				// Then the duplicate burst: all burst goroutines must miss, which
				// the arrival gate guarantees by parking every upstream response
				// until the window fills.
				prompt := completionBody(fmt.Sprintf("r%d-burst", r))
				up.arm(burst)
				var wg sync.WaitGroup
				for i := 0; i < burst; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						_, _ = gatewayProbe(target, prompt)
					}()
				}
				// Join on ARRIVALS, not on responses: releasing after wg.Wait()
				// self-deadlocks, since the parked handlers are what keep http.Do
				// pending.
				if !up.awaitGate() {
					up.releaseNow()
					t.Fatalf("harness: round %d gate never filled (%d of %d arrived)", r, up.arrivedCount(), burst)
				}
				up.releaseNow()
				wg.Wait()
				up.disarm()
				proxy.waitFinalize()

				if dropped := proxy.finalizeDropped.Load(); dropped != 0 {
					t.Fatalf("harness: %d finalize job(s) dropped by round %d; stores not guaranteed", dropped, r)
				}
				entries, _, _, _, _ := proxy.Cache.Stats()
				occupancyByRound = append(occupancyByRound, entries)
			}

			finalOccupancy := occupancyByRound[rounds-1]
			permanentSlotLoss := capacity - finalOccupancy
			peakSlotsLost := 0
			for _, e := range occupancyByRound {
				if l := capacity - e; l > peakSlotsLost {
					peakSlotsLost = l
				}
			}

			// The duplicates must all have paid for their own upstream call or the
			// cell never produced its replacement stores: capacity fill + per round
			// (inserts legitimate + replacements duplicate misses).
			calls := up.calls.Load()
			if want := int64(capacity + rounds*(mx.inserts+mx.replacements)); calls != want {
				t.Fatalf("harness: upstream calls = %d, want %d; the mix did not run as specified", calls, want)
			}

			// MEASURED CELL, logged as requested.
			t.Logf("measured inserts=%d replacements=%d: capacity=%d rounds=%d occupancy %d -> %v; permanent slots lost = %d, peak slots lost = %d, upstream calls = %d",
				mx.inserts, mx.replacements, capacity, rounds, startEntries, occupancyByRound, permanentSlotLoss, peakSlotsLost, calls)

			results = append(results, cellResult{
				m:                 mx,
				permanentSlotLoss: permanentSlotLoss,
				peakSlotsLost:     peakSlotsLost,
				occupancy:         occupancyByRound,
				calls:             calls,
			})

			// DISCRIMINATOR. A correct LRU cache holds capacity under interleaved
			// traffic: fresh keys displace cold ones one-for-one, and a redundant
			// replacement of a resident key displaces nothing. Pre-fix this fails
			// in every cell.
			if finalOccupancy != capacity {
				t.Errorf("inserts=%d replacements=%d: cache occupancy = %d after %d rounds, want %d: %d permanent slot(s) lost to redundant replacements (per-round: %v)",
					mx.inserts, mx.replacements, finalOccupancy, rounds, capacity, permanentSlotLoss, occupancyByRound)
			}

			// CHARACTERIZATION, deliberately non-discriminating: it asks whether
			// the saturating constant can be pushed above one by widening either
			// mix axis. If it ever trips, the damage is a growing leak rather than
			// a fixed one-slot cost and the severity assessment must be rewritten.
			if peakSlotsLost > 1 {
				t.Errorf("inserts=%d replacements=%d: loss exceeded the saturating constant: peak %d slot(s) lost over %d rounds (per-round: %v); expected at most 1",
					mx.inserts, mx.replacements, peakSlotsLost, rounds, occupancyByRound)
			}

			// The burst's response must still be readable: the loss is collateral
			// damage to OTHER entries, not a failed store. Probed only after the
			// measurement, so it cannot perturb the trajectory.
			if !mustProbe(t, target, completionBody(fmt.Sprintf("r%d-burst", rounds-1))) {
				t.Errorf("inserts=%d replacements=%d: last round's burst response is not served from cache", mx.inserts, mx.replacements)
			}
			proxy.waitFinalize()

			// --- Destruction census -------------------------------------------
			// Occupancy only reports how many slots are EMPTY at the end, so a
			// victim destroyed early and backfilled later is invisible to it.
			// This census names every key the workload created and asks which are
			// still resident. It runs in census mode, where a miss receives an
			// empty upstream body, fails the gateway's store guard, and so cannot
			// evict the next victim it is about to count.
			up.censusMode.Store(true)

			coldSurvivors := 0
			for i := 0; i < capacity; i++ {
				if mustProbe(t, target, completionBody(fmt.Sprintf("cold-%d", i))) {
					coldSurvivors++
				}
			}
			roundKeysCreated := rounds * mx.inserts
			roundSurvivors := 0
			for r := 0; r < rounds; r++ {
				for k := 0; k < mx.inserts-1; k++ {
					if mustProbe(t, target, completionBody(fmt.Sprintf("r%d-ins%d", r, k))) {
						roundSurvivors++
					}
				}
				if mustProbe(t, target, completionBody(fmt.Sprintf("r%d-burst", r))) {
					roundSurvivors++
				}
			}
			proxy.waitFinalize()

			coldsDestroyed := capacity - coldSurvivors
			roundsDestroyed := roundKeysCreated - roundSurvivors

			// Census integrity. If a census miss had stored anything, membership
			// would have moved; if the key list were incomplete, the survivors
			// could not account for the occupancy. Either failure makes the
			// destruction counts below meaningless, so it is fatal rather than a
			// mere error.
			if censusOcc, _, _, _, _ := proxy.Cache.Stats(); censusOcc != finalOccupancy {
				t.Fatalf("harness: census changed occupancy %d -> %d; census mode is not store-free", finalOccupancy, censusOcc)
			}
			if coldSurvivors+roundSurvivors != finalOccupancy {
				t.Fatalf("harness: census survivors %d+%d != occupancy %d; the census key list is incomplete",
					coldSurvivors, roundSurvivors, finalOccupancy)
			}

			t.Logf("DESTRUCTION census inserts=%d replacements=%d: colds destroyed=%d/%d, round keys destroyed=%d/%d, total live responses destroyed=%d (occupancy %d, shortfall %d)",
				mx.inserts, mx.replacements, coldsDestroyed, capacity, roundsDestroyed, roundKeysCreated,
				coldsDestroyed+roundsDestroyed, finalOccupancy, permanentSlotLoss)

			results[len(results)-1].coldsDestroyed = coldsDestroyed
			results[len(results)-1].roundsDestroyed = roundsDestroyed
		})
	}

	// MEASURED MATRIX as log output. Excess is destruction beyond the
	// legitimate LRU budget of one eviction per insertion, i.e.
	// rounds*inserts, so a capacity-proportional cost would show up here as a
	// larger excess at capacity 128 than at 8.
	t.Logf("mix matrix at rounds=%d capacities=%v (excess = totalDestroyed - rounds*inserts):", rounds, capacities)
	for _, r := range results {
		legitBudget := rounds * r.m.inserts
		total := r.coldsDestroyed + r.roundsDestroyed
		t.Logf("  capacity=%d inserts=%d replacements=%d -> permanent=%d peak=%d final=%d calls=%d coldsDestroyed=%d/%d roundKeysDestroyed=%d/%d totalDestroyed=%d excess=%d",
			r.m.capacity, r.m.inserts, r.m.replacements, r.permanentSlotLoss, r.peakSlotsLost,
			r.m.capacity-r.permanentSlotLoss, r.calls, r.coldsDestroyed, r.m.capacity,
			r.roundsDestroyed, rounds*r.m.inserts, total, total-legitBudget)
	}

	if len(results) != len(mixes) {
		t.Fatalf("harness: %d of %d mix cells produced a measurement", len(results), len(mixes))
	}

	// GENERALIZATION CLAIM UNDER TEST, part one: the saturating constant must be
	// invariant across both mix axes. On the fixed tree every row is 0, so
	// equality there is trivial; the claim is carried by the unfixed-baseline run
	// of this same file, where every row is expected to read permanent=1 peak=1.
	first := results[0]
	for _, r := range results[1:] {
		if r.permanentSlotLoss != first.permanentSlotLoss || r.peakSlotsLost != first.peakSlotsLost {
			t.Errorf("loss depends on the mix, not saturating at a constant: cell (cap=%d,ins=%d,rep=%d) gave permanent=%d peak=%d, but cell (cap=%d,ins=%d,rep=%d) gave permanent=%d peak=%d",
				r.m.capacity, r.m.inserts, r.m.replacements, r.permanentSlotLoss, r.peakSlotsLost,
				first.m.capacity, first.m.inserts, first.m.replacements, first.permanentSlotLoss, first.peakSlotsLost)
		}
	}

	// GENERALIZATION CLAIM UNDER TEST, part two: the EXCESS destruction must not
	// grow with capacity. Pair each cell at capacity 8 with the same mix at the
	// other capacities and require the same excess and the same permanent loss.
	// This is the claim that a constant measured at capacity 8 was an artifact of
	// the small fixture: at 128 the eviction budget is a twelfth of the cold
	// reservoir, so if the cost scaled with capacity it would surface here.
	key := func(m mix) string { return fmt.Sprintf("ins%d_rep%d", m.inserts, m.replacements) }
	byMix := map[string][]cellResult{}
	for _, r := range results {
		byMix[key(r.m)] = append(byMix[key(r.m)], r)
	}
	for mk, rows := range byMix {
		base := rows[0]
		for _, r := range rows[1:] {
			baseExcess := base.coldsDestroyed + base.roundsDestroyed - rounds*base.m.inserts
			rExcess := r.coldsDestroyed + r.roundsDestroyed - rounds*r.m.inserts
			if rExcess != baseExcess || r.permanentSlotLoss != base.permanentSlotLoss {
				t.Errorf("cost scales with capacity for mix %s: capacity=%d gave excess=%d permanent=%d, but capacity=%d gave excess=%d permanent=%d",
					mk, base.m.capacity, baseExcess, base.permanentSlotLoss, r.m.capacity, rExcess, r.permanentSlotLoss)
			}
		}
	}
}
