package core

import (
	"testing"
	"time"

	"github.com/wrongstack/wrongtrace/internal/db"
)

// TestGetRecentEventsFiltered_Cache verifies the gen+TTL memoization for
// since-less, file-less queries (the dashboard's every-10s poll shape) and
// that cursored (since) queries always bypass the cache and stay exact.
func TestGetRecentEventsFiltered_Cache(t *testing.T) {
	eng, store := newTestEngine(t)

	insert := func(id string, at time.Time) {
		t.Helper()
		if err := store.InsertEvent(db.EventRecord{
			EventID:    id,
			RepoName:   "wrongtrace",
			FilePath:   "internal/core/engine.go",
			Signature:  "func:engine.go::" + id,
			NodeType:   "function",
			Action:     "ADDED",
			OccurredAt: at.UTC(),
		}); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}

	base := time.Now().UTC()
	insert("ev-1", base.Add(-3*time.Second))
	insert("ev-2", base.Add(-2*time.Second))

	// First call populates the cache.
	first, err := eng.GetRecentEventsFiltered(50, "wrongtrace", "", time.Time{})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("first: got %d events, want 2", len(first))
	}

	// A store write the engine does not know about (no BumpCacheGen) must
	// NOT be visible while the cache entry is fresh — this is the memoization
	// the every-10s poll relies on.
	insert("ev-3", base.Add(-1*time.Second))
	cached, err := eng.GetRecentEventsFiltered(50, "wrongtrace", "", time.Time{})
	if err != nil {
		t.Fatalf("cached: %v", err)
	}
	if len(cached) != 2 {
		t.Fatalf("cached: got %d events, want 2 (stale-by-design within TTL)", len(cached))
	}

	// A new event bumps the generation; the cache must invalidate.
	eng.BumpCacheGen()
	fresh, err := eng.GetRecentEventsFiltered(50, "wrongtrace", "", time.Time{})
	if err != nil {
		t.Fatalf("fresh: %v", err)
	}
	if len(fresh) != 3 {
		t.Fatalf("fresh: got %d events, want 3 after gen bump", len(fresh))
	}

	// Cursored queries bypass the cache entirely: even without a gen bump
	// they must see the very latest rows so incremental fetches stay exact.
	cursor := base.Add(-2 * time.Second) // matches/after ev-2's timestamp
	since, err := eng.GetRecentEventsFiltered(50, "wrongtrace", "", cursor)
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	// ev-2 and ev-3 are at-or-after the cursor; ev-1 is older.
	if len(since) != 2 {
		t.Fatalf("since: got %d events, want 2 (cursor must bypass cache): %+v", len(since), since)
	}

	// File-scoped queries also bypass the cache (different query shape).
	fileScoped, err := eng.GetRecentEventsFiltered(50, "wrongtrace", "internal/core/engine.go", time.Time{})
	if err != nil {
		t.Fatalf("fileScoped: %v", err)
	}
	if len(fileScoped) != 3 {
		t.Fatalf("fileScoped: got %d events, want 3", len(fileScoped))
	}
}

func TestBumpCacheGenReleasesStalePayloads(t *testing.T) {
	eng, _ := newTestEngine(t)
	eng.atlasCache = map[string]cachedAtlas{"old-project": {gen: eng.cacheGen}}
	eng.metricsCache = map[string]cachedMetrics{"old-project": {gen: eng.cacheGen}}
	eng.recentCache = map[string]cachedRecent{
		"500\x00old-project": {
			gen:    eng.cacheGen,
			events: make([]db.EventRecord, 500),
		},
	}

	before := eng.cacheGen
	eng.BumpCacheGen()

	if eng.cacheGen != before+1 {
		t.Fatalf("cache generation = %d, want %d", eng.cacheGen, before+1)
	}
	if eng.atlasCache != nil || eng.metricsCache != nil || eng.recentCache != nil {
		t.Fatal("generation bump retained stale cache payloads")
	}
}
