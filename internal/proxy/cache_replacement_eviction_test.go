package proxy

import (
	"fmt"
	"testing"
	"time"
)

// Response cache capacity regression suite for the "replacement makes room for
// nothing" contract.
//
// SetWithReplayHeader used to run its capacity-eviction block on EVERY store,
// including a store for a key that was already resident. A replacement does
// not grow len(items), so no room is needed — yet an unrelated live entry was
// still shed whenever the replaced key happened not to be the coldest one.
// The gateway has no in-flight dedup: N concurrent identical requests all miss,
// all fetch upstream, then all store the same key, so one duplicate burst could
// destroy up to N-1 live cached responses.
//
// These tests pin the fixed behavior and, crucially, the negative space: they
// assert eviction is NOT disabled for genuine insertions.

// TestResponseCache_ReplacementAtCapacityKeepsUnrelatedEntry is the direct
// single-store reproduction: the replaced key is NOT the coldest resident, so
// the pre-fix eviction pass destroyed a different, unrelated live entry.
//
// The recency arrangement matters. If the replaced key happens to be the
// coldest entry, the buggy code picks it as the victim and then overwrites it
// anyway — self-eviction, a silent no-op that passes with or without the fix.
// So J must stay the coldest resident while the duplicate store targets the
// hot key M, which is what makes this test a real discriminator.
//
// Liveness is asserted through occupancy/identity rather than an extra Get,
// because Get itself refreshes recency and would move the would-be victim.
func TestResponseCache_ReplacementAtCapacityKeepsUnrelatedEntry(t *testing.T) {
	cache := NewResponseCache(3, time.Hour)
	set := func(key string) {
		t.Helper()
		cache.Set(key, "provider", "model", 200, nil, []byte("body-"+key), false, 0, 0, time.Hour)
	}

	// Fill to capacity: J is inserted second and never read again, so it is
	// the least-recently-used resident.
	set("K")
	set("J")
	set("M")
	if _, ok := cache.Get("K"); !ok {
		t.Fatal("harness: K not resident")
	}
	if _, ok := cache.Get("M"); !ok {
		t.Fatal("harness: M not resident")
	}
	// Recency order is now J (coldest) < K < M. Confirm the fixture without
	// touching any stamp.
	if entries, _, _, _, _ := cache.Stats(); entries != 3 {
		t.Fatalf("harness: occupancy = %d, want 3 before the replacement", entries)
	}

	// The duplicate store: M is ALREADY resident, so the map does not grow
	// and no room has to be made.
	set("M")

	if _, ok := cache.Get("M"); !ok {
		t.Error("replaced entry M missing from the cache")
	}
	// The defect: an unrelated live response paid for M's redundant re-store.
	// K is read here first so a failure names J unambiguously.
	if _, ok := cache.Get("K"); !ok {
		t.Error("live entry K was evicted by a redundant replacement of M")
	}
	if entries, _, _, _, _ := cache.Stats(); entries != 3 {
		t.Errorf("occupancy = %d after a replacement, want 3; the coldest unrelated entry J was evicted to make room that was not needed", entries)
	}
}

// TestResponseCache_DuplicateStoreBurstDoesNotDrainCache proves the
// amplification: pre-fix each redundant store shed one more live entry, so a
// burst collapsed occupancy instead of leaving it flat.
func TestResponseCache_DuplicateStoreBurstDoesNotDrainCache(t *testing.T) {
	const capacity = 8
	cache := NewResponseCache(capacity, time.Hour)
	set := func(key string) {
		t.Helper()
		cache.Set(key, "provider", "model", 200, nil, []byte("body-"+key), false, 0, 0, time.Hour)
	}

	for i := 0; i < capacity; i++ {
		set(fmt.Sprintf("fill-%d", i))
	}
	set("dup") // fills capacity, shedding the single coldest entry

	before, _, _, _, _ := cache.Stats()
	if before != capacity {
		t.Fatalf("harness: occupancy = %d, want %d before the burst", before, capacity)
	}

	for i := 0; i < 6; i++ {
		set("dup") // every one of these is a replacement of a resident key
	}

	after, _, _, _, _ := cache.Stats()
	if after != before {
		t.Errorf("occupancy went %d -> %d across 6 redundant stores of one key; want it unchanged", before, after)
	}
	if _, ok := cache.Get("dup"); !ok {
		t.Error("the repeatedly replaced key itself was lost")
	}
}

// TestResponseCache_ReplacementStillUpdatesValue guards the actual purpose of
// the store: skipping eviction must not skip the update. Also covers the
// boundary where the replaced key IS the coldest entry, i.e. the pre-fix code
// would have "evicted" the very slot it was about to rewrite.
func TestResponseCache_ReplacementStillUpdatesValue(t *testing.T) {
	cache := NewResponseCache(3, time.Hour)
	set := func(key, body string) {
		t.Helper()
		cache.Set(key, "provider", "model", 200, nil, []byte(body), false, 0, 0, time.Hour)
	}

	set("cold", "v1") // coldest: never read again
	set("mid", "x")
	set("hot", "x")
	if _, ok := cache.Get("hot"); !ok {
		t.Fatal("harness: hot not resident")
	}
	if _, ok := cache.Get("mid"); !ok {
		t.Fatal("harness: mid not resident")
	}

	set("cold", "v2") // replacement of the coldest key
	got, ok := cache.Get("cold")
	if !ok {
		t.Fatal("replaced key disappeared")
	}
	if string(got.Body) != "v2" {
		t.Errorf("stored body = %q, want %q (replacement must update the value)", got.Body, "v2")
	}
	if entries, _, _, _, _ := cache.Stats(); entries != 3 {
		t.Errorf("occupancy = %d, want 3", entries)
	}
}

// TestResponseCache_GenuineInsertionAtCapacityStillEvicts is the negative-space
// guard: the fix must not have disabled eviction for keys that are NOT resident.
func TestResponseCache_GenuineInsertionAtCapacityStillEvicts(t *testing.T) {
	cache := NewResponseCache(2, time.Hour)
	set := func(key string) {
		t.Helper()
		cache.Set(key, "provider", "model", 200, nil, []byte("body-"+key), false, 0, 0, time.Hour)
	}

	set("A")
	set("B")
	if _, ok := cache.Get("A"); !ok { // A hot, B cold
		t.Fatal("harness: A not resident")
	}
	set("C") // genuine insertion at capacity: must shed B

	if _, ok := cache.Get("C"); !ok {
		t.Error("newly inserted entry C missing")
	}
	if _, ok := cache.Get("A"); !ok {
		t.Error("hot entry A was evicted; LRU must keep the most recently used entry")
	}
	if _, ok := cache.Get("B"); ok {
		t.Error("cold entry B survived; eviction must still run for real insertions")
	}
	if entries, _, _, _, _ := cache.Stats(); entries != 2 {
		t.Errorf("occupancy = %d, want 2 (capacity must still be enforced)", entries)
	}
}

// TestResponseCache_ReplacementDoesNotResurrectExpiredEntry covers the
// secondary branch: because a replacement now skips the opportunistic expired
// purge, an expired entry must still be invisible to readers (it is dropped
// lazily by Get) and occupancy must never exceed capacity.
func TestResponseCache_ReplacementDoesNotResurrectExpiredEntry(t *testing.T) {
	cache := NewResponseCache(2, time.Hour)
	setTTL := func(key string, ttl time.Duration) {
		t.Helper()
		cache.Set(key, "provider", "model", 200, nil, []byte("body-"+key), false, 0, 0, ttl)
	}

	setTTL("doomed", time.Nanosecond)
	setTTL("keep", time.Hour)
	time.Sleep(5 * time.Millisecond) // let "doomed" expire

	setTTL("doomed", time.Hour) // replacement of a resident-but-expired key
	if _, ok := cache.Get("doomed"); !ok {
		t.Error("re-stored key is not readable; replacement must refresh the entry")
	}
	if entries, _, _, _, _ := cache.Stats(); entries > 2 {
		t.Errorf("occupancy = %d, exceeds capacity 2", entries)
	}
	if _, ok := cache.Get("keep"); !ok {
		t.Error("unrelated live entry lost")
	}
}
