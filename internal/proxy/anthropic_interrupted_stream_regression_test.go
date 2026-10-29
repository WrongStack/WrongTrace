package proxy

// Audit F221: an Anthropic Messages stream that ends before message_delta
// (client pressed Esc / cancelled, or the connection dropped) is recorded with
// CompletionTokens = 1 -- the placeholder output_tokens that message_start
// always carries -- even though thousands of characters of reply were streamed
// and are billed by the provider. The OpenAI-shaped control (no usage seen)
// falls back to the text estimate.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wrongstack/wrongtrace/internal/ipc"
)

type anthCutReporter struct {
	mu   sync.Mutex
	runs []ipc.TelemetryReport
}

func (r *anthCutReporter) ReportRun(p ipc.TelemetryReport) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs = append(r.runs, p)
	return nil
}

const anthCutDeltas = 60

// anthCutRun streams frames from a fake upstream through the proxy. When cut is
// true the client cancels right after the last content frame arrives and the
// upstream never sends the terminal frames.
func anthCutRun(t *testing.T, path string, head string, delta func(i int) string, tail string, cut bool) (ProxyTrafficRecord, ipc.TelemetryReport) {
	t.Helper()
	for _, k := range []string{"WRONGTRACE_HOME", "HOME", "USERPROFILE", "APPDATA"} {
		t.Setenv(k, t.TempDir())
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		_, _ = io.WriteString(w, head)
		for i := 0; i < anthCutDeltas; i++ {
			_, _ = io.WriteString(w, delta(i))
		}
		_, _ = io.WriteString(w, ": LAST-CONTENT-FRAME\n\n")
		f.Flush()
		if cut {
			<-r.Context().Done() // gated: only the client's cancel ends this
			return
		}
		_, _ = io.WriteString(w, tail)
		f.Flush()
	}))
	defer up.Close()

	got := make(chan ProxyTrafficRecord, 1)
	rep := &anthCutReporter{}
	p := NewGatewayProxy(Config{Reporter: rep, CustomUpstreams: map[string]string{"fake": up.URL},
		OnTraffic: func(rec ProxyTrafficRecord) { got <- rec }})
	defer p.Close()
	srv := httptest.NewServer(p)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := `{"model":"claude-3-7-sonnet","max_tokens":4096,"stream":true,"messages":[{"role":"user","content":"write a long story"}]}`
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Session-ID", "anthCut-run")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if cut && strings.Contains(sc.Text(), "LAST-CONTENT-FRAME") {
			cancel() // user interrupts after the whole reply text was streamed
			break
		}
	}
	resp.Body.Close()

	select {
	case rec := <-got:
		rep.mu.Lock()
		defer rep.mu.Unlock()
		if len(rep.runs) != 1 {
			t.Fatalf("runs=%d", len(rep.runs))
		}
		return rec, rep.runs[0]
	case <-time.After(10 * time.Second):
		t.Fatal("no traffic record")
	}
	return ProxyTrafficRecord{}, ipc.TelemetryReport{}
}

func TestProxy_AnthropicInterruptedStreamEstimatesOutput(t *testing.T) {
	word := strings.Repeat("lorem ipsum ", 4) // 48 chars per delta
	replyChars := len(word) * anthCutDeltas
	estimate := int64(float64(replyChars) / 3.7)

	anthHead := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_anthCut\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-3-7-sonnet\",\"content\":[],\"usage\":{\"input_tokens\":500,\"output_tokens\":1}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"
	anthDelta := func(int) string {
		return "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"" + word + "\"}}\n\n"
	}
	anthTail := "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":800}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

	oaiDelta := func(i int) string {
		return fmt.Sprintf("data: {\"id\":\"chatcmpl-anthCut\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"%s\"}}]}\n\n", word)
	}

	// CONTROL 1: complete Anthropic stream -> message_delta usage is used.
	full, _ := anthCutRun(t, "/proxy/fake/v1/messages", anthHead, anthDelta, anthTail, false)
	t.Logf("CONTROL complete anthropic stream: CompletionTokens=%d (want 800) truncated=%v", full.CompletionTokens, full.Truncated)
	// CONTROL 2: interrupted OpenAI stream -> text estimate.
	oai, oaiRun := anthCutRun(t, "/proxy/fake/v1/chat/completions", "", oaiDelta, "data: [DONE]\n\n", true)
	t.Logf("CONTROL interrupted openai stream: CompletionTokens=%d run.CompletionTokens=%d (estimate ~%d) truncated=%v", oai.CompletionTokens, oaiRun.CompletionTokens, estimate, oai.Truncated)
	if full.CompletionTokens != 800 || oai.CompletionTokens < estimate/2 || !oai.Truncated {
		t.Fatalf("INVALID PROOF: control failed")
	}

	// SUBJECT: interrupted Anthropic stream, same streamed reply text.
	cut, cutRun := anthCutRun(t, "/proxy/fake/v1/messages", anthHead, anthDelta, anthTail, true)
	t.Logf("streamed reply chars=%d", replyChars)
	t.Logf("EXPECTED: CompletionTokens >= ~%d (text estimate, like the OpenAI control); cost includes the streamed output", estimate)
	t.Logf("ACTUAL:   rec.CompletionTokens=%d rec.CostUSD=%.6f run.CompletionTokens=%d run.CostUSD=%.6f truncated=%v",
		cut.CompletionTokens, cut.CostUSD, cutRun.CompletionTokens, cutRun.CostUSD, cut.Truncated)
	if cut.Truncated && cut.CompletionTokens < estimate/2 {
		t.Fatal("interrupted Anthropic stream kept the message_start placeholder as its output count")
	}
}
