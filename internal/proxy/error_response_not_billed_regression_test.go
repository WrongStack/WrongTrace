package proxy

// Regression (audit F141): an upstream error response (non-2xx) is declared non-billable by
// finalize (quota refunded), yet the traffic record and the run totals reported
// to the engine still carry an estimated, non-zero cost and token counts.

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/wrongstack/wrongtrace/internal/ipc"
)

type f141Reporter struct {
	mu   sync.Mutex
	runs []ipc.TelemetryReport
}

func (r *f141Reporter) ReportRun(p ipc.TelemetryReport) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs = append(r.runs, p)
	return nil
}

func f141Run(t *testing.T, status int, body string) (ProxyTrafficRecord, ipc.TelemetryReport, float64) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	defer up.Close()
	rep := &f141Reporter{}
	p := NewGatewayProxy(Config{Reporter: rep, CustomUpstreams: map[string]string{"quota": up.URL}})
	defer p.Close()
	srv := httptest.NewServer(p)
	defer srv.Close()
	p.Quotas.SetBudget("f141-tenant", 100)

	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"` + string(bytes.Repeat([]byte("x"), 40000)) + `"}]}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/proxy/quota/v1/chat/completions", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Project-Slug", "f141-tenant")
	req.Header.Set("X-WrongTrace-Policy", "enforce")
	req.Header.Set("X-Session-ID", "f141-run")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != status {
		t.Fatalf("status %d want %d", resp.StatusCode, status)
	}
	p.waitFinalize()
	recs := p.AllTraffic(1)
	if len(recs) != 1 || len(rep.runs) != 1 {
		t.Fatalf("records=%d runs=%d", len(recs), len(rep.runs))
	}
	spend, _ := p.Quotas.GetSpend("f141-tenant")
	return recs[0], rep.runs[0], spend
}

func TestErrorResponsesAreNotBilled(t *testing.T) {
	// CONTROL: a 2xx exchange is billable, recorded cost == quota spend.
	okRec, okRun, okSpend := f141Run(t, http.StatusOK,
		`{"id":"c1","model":"gpt-4o","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10000,"completion_tokens":5}}`)
	t.Logf("CONTROL (200): rec.CostUSD=%.6f run.CostUSD=%.6f quotaSpend=%.6f -> consistent=%v\n",
		okRec.CostUSD, okRun.CostUSD, okSpend, okRec.CostUSD > 0 && okRun.CostUSD == okRec.CostUSD)

	failed := false
	for _, tc := range []struct {
		status int
		body   string
	}{
		{http.StatusTooManyRequests, `{"error":{"message":"Rate limit reached","type":"requests","code":"rate_limit_exceeded"}}`},
		{http.StatusUnauthorized, `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error"}}`},
		{http.StatusInternalServerError, `{"error":{"message":"boom"}}`},
	} {
		rec, run, spend := f141Run(t, tc.status, tc.body)
		t.Logf("HTTP %d EXPECTED: rec.CostUSD=0 run.CostUSD=0 run.PromptTokens=0 (quota treats it as non-billable: spend=%.6f)\n", tc.status, spend)
		t.Logf("HTTP %d ACTUAL:   rec.CostUSD=%.6f rec.PromptTokens=%d run.CostUSD=%.6f run.PromptTokens=%d run.CompletionTokens=%d\n",
			tc.status, rec.CostUSD, rec.PromptTokens, run.CostUSD, run.PromptTokens, run.CompletionTokens)
		if spend == 0 && (rec.CostUSD > 0 || run.CostUSD > 0 || run.PromptTokens > 0) {
			failed = true
		}
	}
	if failed {
		t.Log("PROBLEM CONFIRMED")
		t.FailNow()
	}
	t.Log("PROBLEM NOT REPRODUCED")
}
