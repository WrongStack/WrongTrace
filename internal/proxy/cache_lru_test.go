package proxy

import (
	"testing"
	"time"
)

// TestResponseCache_EvictsColdNotHot pins the round-40 contract: the cache
// documents LRU, so at capacity the entry with no recent hits must be evicted
// while a hot (recently read) entry survives — even when the hot entry was
// inserted FIRST. The pre-fix implementation evicted by insertion order
// (FIFO), discarding exactly the traffic the cache exists to absorb.
//
// Recency stamps come from the cache's logical clock (strictly increasing per
// Set/Get), so the test is deterministic regardless of wall-clock granularity.
func TestResponseCache_EvictsColdNotHot(t *testing.T) {
	cache := NewResponseCache(2, time.Hour)
	set := func(key string) {
		t.Helper()
		cache.Set(key, "provider", "model", 200, nil, []byte("body-"+key), false, 0, 0, time.Hour)
	}

	set("A")
	set("B")
	if _, ok := cache.Get("A"); !ok {
		t.Fatalf("harness: hot entry A missed before eviction")
	}
	set("C") // capacity hit

	if _, ok := cache.Get("A"); !ok {
		t.Errorf("hot entry A was evicted at capacity; LRU must keep the most recently used entry")
	}
	if _, ok := cache.Get("B"); ok {
		t.Errorf("cold entry B survived the capacity eviction; LRU must shed the least recently used entry")
	}
	if _, ok := cache.Get("C"); !ok {
		t.Errorf("newly inserted entry C missing after eviction")
	}
}

// TestResponseCache_NoTrafficEvictsOldestInsertion pins the degenerate case:
// with no Get traffic every stamp ties at its insertion, so eviction falls
// back to oldest-inserted — FIFO and LRU agree when nothing is ever read.
func TestResponseCache_NoTrafficEvictsOldestInsertion(t *testing.T) {
	cache := NewResponseCache(2, time.Hour)
	set := func(key string) {
		t.Helper()
		cache.Set(key, "provider", "model", 200, nil, []byte("body-"+key), false, 0, 0, time.Hour)
	}

	set("X")
	set("Y")
	set("Z")
	if _, ok := cache.Get("X"); ok {
		t.Errorf("untouched oldest entry X survived eviction")
	}
	if _, ok := cache.Get("Y"); !ok {
		t.Errorf("untouched Y should have survived over oldest X")
	}
}

// TestResponseCache_CapacityOne pins the capacity-1 boundary: any later Set
// displaces the previous entry regardless of eviction policy.
func TestResponseCache_CapacityOne(t *testing.T) {
	cache := NewResponseCache(1, time.Hour)
	set := func(key string) {
		t.Helper()
		cache.Set(key, "provider", "model", 200, nil, []byte("body-"+key), false, 0, 0, time.Hour)
	}

	set("only")
	set("next")
	if _, ok := cache.Get("only"); ok {
		t.Errorf("capacity-1: first entry survived a later Set")
	}
	if _, ok := cache.Get("next"); !ok {
		t.Errorf("capacity-1: newest entry missing")
	}
}
