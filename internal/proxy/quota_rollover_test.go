package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestQuotaSettlement_DoesNotCrossUTCDay pins the daily-meter contract across
// a UTC reset. A pre-midnight reservation is settled after a new-day request
// has already been metered; the stale correction must affect neither the key's
// nor the global total for the new day.
func TestQuotaSettlement_DoesNotCrossUTCDay(t *testing.T) {
	t.Run("same day reconciliation remains exact", func(t *testing.T) {
		q := NewQuotaLimiter()
		q.SetBudget("project", 100)
		allowed, _, _ := q.CheckAndRecordSpend("project", 10)
		if !allowed {
			t.Fatal("setup: reservation denied")
		}
		q.AdjustSpend("project", -3) // actual cost was 7
		if spend, _ := q.GetSpend("project"); spend != 7 {
			t.Fatalf("same-day spend = %v, want 7", spend)
		}
	})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-rollover","model":"gpt-4o","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer upstream.Close()

	entered := make(chan ProxyTrafficRecord, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseFinalizer := func() { releaseOnce.Do(func() { close(release) }) }

	p := NewGatewayProxy(Config{
		OnTraffic: func(rec ProxyTrafficRecord) {
			entered <- rec
			<-release
		},
	})
	t.Cleanup(p.Close)
	t.Cleanup(releaseFinalizer) // LIFO: release the worker before p.Close waits.
	p.Quotas.SetBudget("project", 1000)

	dayOne := time.Date(2026, time.January, 1, 23, 59, 59, 0, time.UTC)
	dayTwo := dayOne.Add(2 * time.Second)
	clock := dayOne
	p.Quotas.now = func() time.Time { return clock }

	body := `{"model":"gpt-4o","max_tokens":100000,"messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("X-Target-Upstream", upstream.URL)
	req.Header.Set("X-WrongTrace-Policy", "enforce")
	req.Header.Set("X-Project-Slug", "project")
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	var rec ProxyTrafficRecord
	select {
	case rec = <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("finalizer did not reach OnTraffic")
	}

	reserved, _ := p.Quotas.GetSpend("project")
	if reserved <= rec.CostUSD {
		t.Fatalf("setup: reservation %v must exceed actual cost %v", reserved, rec.CostUSD)
	}

	// Cross UTC midnight while the old request is paused immediately before
	// settlement, then independently meter a new-day request.
	clock = dayTwo
	p.Quotas.RecordSpend("project", 7)
	releaseFinalizer()
	p.waitFinalize()

	if spend, _ := p.Quotas.GetSpend("project"); spend != 7 {
		t.Errorf("new-day project spend = %v, want 7; stale reservation correction corrupted today's meter", spend)
	}
	if spend, _ := p.Quotas.GetSpend(globalBudgetKey); spend != 7 {
		t.Errorf("new-day global spend = %v, want 7; stale reservation correction corrupted today's global meter", spend)
	}
}
