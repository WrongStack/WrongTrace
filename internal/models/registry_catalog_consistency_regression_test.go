package models

// Get computes a fuzzy alias under RLock, RELEASES the lock, then
// re-acquires the write lock to insert the result into aliasCache. A catalog
// mutation (ImportModelsDevJSON / Upsert) that lands in that gap clears the
// cache, and Get then inserts its PRE-mutation result into the fresh cache.
// The stale price is served for that alias until the next catalog mutation.
//
// Ordering is gated deterministically through the registry's own RWMutex:
//   T holds r.mu.Lock -> G (Get) parks in RLock (already counted as a reader)
//   -> M (mutation) parks in rw.w -> T unlocks. M must now wait for G's read
//   phase, and G's later Lock must wait for M. No sleeps; goroutine states are
//   observed from runtime.Stack wait reasons.

import (
	"runtime"
	"strings"
	"testing"
)

func waitParked(t *testing.T, fn, reason string) {
	t.Helper()
	buf := make([]byte, 1<<20)
	for i := 0; i < 5_000_000; i++ {
		n := runtime.Stack(buf, true)
		for _, g := range strings.Split(string(buf[:n]), "\n\n") {
			hdr, _, _ := strings.Cut(g, "\n")
			if strings.Contains(hdr, "["+reason) && strings.Contains(g, fn) {
				return
			}
		}
		runtime.Gosched()
	}
	t.Fatalf("goroutine %s never parked in %s", fn, reason)
}

func raceAliasWithMutation(t *testing.T, r *Registry, alias string, mutate func(), mutName string) ModelInfo {
	r.mu.Lock()
	gDone := make(chan ModelInfo)
	go func() { m, _ := r.Get(alias); gDone <- m }()
	waitParked(t, "models.(*Registry).Get", "sync.RWMutex.RLock")
	mDone := make(chan struct{})
	go func() { mutate(); close(mDone) }()
	waitParked(t, "models.(*Registry)."+mutName, "sync.")
	r.mu.Unlock()
	during := <-gDone
	<-mDone
	return during
}

const raceCatalogJSON = `{"openai":{"name":"OpenAI","models":{"openai/gpt-4o":{"name":"GPT-4o","cost":{"input":3,"output":12,"cache_read":1.5},"limit":{"context":128000}}}}}`

func TestGet_AliasCacheNotStaleAfterConcurrentMutation(t *testing.T) {
	failed := false

	// Variant A: the startup models.dev sync (ImportModelsDevJSON) racing a cost lookup.
	{
		r := NewDefaultRegistry()
		alias := "gpt-4o-2024-08-06"
		raceAliasWithMutation(t, r, alias, func() {
			if _, err := r.ImportModelsDevJSON([]byte(raceCatalogJSON)); err != nil {
				t.Error(err)
			}
		}, "ImportModelsDevJSON")
		after, _ := r.Get(alias)
		bare, _ := r.Get("gpt-4o")
		ctl, _ := r.Get("gpt-4o-2024-11-20") // CONTROL: alias not in flight during the import
		if ctl.InputPricePerM != 3 || bare.InputPricePerM != 3 {
			t.Fatalf("CONTROL FAILED")
		}
		if after.InputPricePerM != 3 {
			failed = true
		}
	}

	// Variant B: a POST /api/models/catalog price update (Upsert) racing a lookup.
	{
		r := NewDefaultRegistry()
		alias := "gpt-4o-mini-2024-07-18"
		raceAliasWithMutation(t, r, alias, func() {
			r.Upsert(ModelInfo{ID: "openai/gpt-4o-mini", ModelID: "gpt-4o-mini", Name: "GPT-4o Mini", Provider: "OpenAI", ProviderID: "openai", InputPricePerM: 0.40, OutputPricePerM: 1.60, IsCanonical: true, IsCustom: true})
		}, "Upsert")
		after, _ := r.Get(alias)
		ctl, _ := r.Get("gpt-4o-mini-2099-01-01") // CONTROL
		if ctl.InputPricePerM != 0.40 {
			t.Fatalf("CONTROL FAILED")
		}
		if after.InputPricePerM != 0.40 {
			failed = true
		}
	}

	if failed {
		t.Fatal("stale alias cache entry survived a catalog mutation")
	}
}

// A reseller entry must not take over the provider-less name its owning
// provider resolves; dated and bare spellings of one model agree on price.
func TestUpsert_ResellerDoesNotRepriceBareName(t *testing.T) {
	r := NewDefaultRegistry()
	owner, _ := r.Get("openai/gpt-4o")
	r.Upsert(ModelInfo{ID: "openrouter/gpt-4o", ModelID: "gpt-4o", Name: "GPT-4o", Provider: "OpenRouter", ProviderID: "openrouter", InputPricePerM: 5, OutputPricePerM: 15})
	bare, _ := r.Get("gpt-4o")
	dated, _ := r.Get("gpt-4o-2024-08-06")
	if bare.ID != owner.ID || dated.ID != owner.ID {
		t.Fatalf("bare -> %s, dated -> %s; want both %s", bare.ID, dated.ID, owner.ID)
	}
	// Control: a provider-less override still takes the bare name.
	r.Upsert(ModelInfo{ID: "gpt-4o", Name: "custom", InputPricePerM: 1, IsCustom: true})
	if m, _ := r.Get("gpt-4o"); m.InputPricePerM != 1 {
		t.Fatalf("provider-less override ignored: %+v", m)
	}
}

// An entry saved under a mixed-case ID is the one its own ID looks up, and it
// replaces (not duplicates) the existing lowercase entry.
func TestUpsert_MixedCaseIDIsFoundByItsOwnID(t *testing.T) {
	r := NewDefaultRegistry()
	r.Upsert(ModelInfo{ID: "OpenAI/gpt-4o", ModelID: "gpt-4o", Name: "GPT-4o", Provider: "OpenAI", ProviderID: "openai", InputPricePerM: 9, IsCanonical: true, IsCustom: true})
	if m, _ := r.Get("OpenAI/gpt-4o"); m.InputPricePerM != 9 {
		t.Fatalf("Get(own id) = %+v; want the $9 override", m)
	}
	if m, _ := r.GetWithProvider("OpenAI", "gpt-4o"); m.InputPricePerM != 9 {
		t.Fatalf("GetWithProvider = %+v; want the $9 override", m)
	}
	n := 0
	prov, _ := r.GetProvider("openai")
	for _, m := range prov.Models {
		if m.ModelID == "gpt-4o" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("openai catalog lists gpt-4o %d times", n)
	}
}
