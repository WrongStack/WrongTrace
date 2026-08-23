package proxy

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/wrongstack/wrongtrace/internal/ipc"
	"github.com/wrongstack/wrongtrace/internal/models"
)

// Reporter abstracts the engine method needed to record telemetry.
type Reporter interface {
	ReportRun(p ipc.TelemetryReport) error
}

// ProxyTrafficRecord stores raw in-flight / completed request and response data passing through the AI gateway.
type ProxyTrafficRecord struct {
	ID               string            `json:"id"`
	Timestamp        time.Time         `json:"timestamp"`
	DurationMs       int64             `json:"duration_ms"`
	Method           string            `json:"method"`
	IncomingPath     string            `json:"incoming_path"`
	TargetURL        string            `json:"target_url"`
	Provider         string            `json:"provider"`
	Model            string            `json:"model"`
	AgentName        string            `json:"agent_name"`
	TaskID           string            `json:"task_id"`
	RunID            string            `json:"run_id,omitempty"`
	SessionKey       string            `json:"session_key,omitempty"`
	ProjectID        string            `json:"project_id,omitempty"`
	ProjectSlug      string            `json:"project_slug,omitempty"`
	StatusCode       int               `json:"status_code"`
	IsStream         bool              `json:"is_stream"`
	RequestHeaders   map[string]string `json:"request_headers"`
	RequestBody      string            `json:"request_body"`
	ResponseHeaders  map[string]string `json:"response_headers"`
	ResponseBody     string            `json:"response_body"`
	PromptTokens     int64             `json:"prompt_tokens"`
	CompletionTokens int64             `json:"completion_tokens"`
	TotalTokens      int64             `json:"total_tokens"`
	CachedTokens     int64             `json:"cached_tokens"`
	ReasoningTokens  int64             `json:"reasoning_tokens"`
	CacheHitRate     float64           `json:"cache_hit_rate"`
	CostUSD          float64           `json:"cost_usd"`
	CacheSavingsUSD  float64           `json:"cache_savings_usd"`
	ToolCalls        []ProxyToolCall   `json:"tool_calls,omitempty"`
	ToolCount        int               `json:"tool_count"`
	AssistantReply   string            `json:"assistant_reply,omitempty"`
	Reasoning        string            `json:"reasoning,omitempty"`
	SystemPrompt     string            `json:"system_prompt,omitempty"`
	MessageCount     int               `json:"message_count"`
	FinishReason     string            `json:"finish_reason,omitempty"`
}

// Config configures the transparent LLM gateway proxy.
type Config struct {
	Reporter        Reporter
	CustomUpstreams map[string]string // providerName -> baseURL
	OnTraffic       func(rec ProxyTrafficRecord)
}

type proxySessionTracker struct {
	runID            string
	promptTokens     int64
	completionTokens int64
	costUSD          float64
	lastSeen         time.Time
	createdAt        time.Time
}

// GatewayProxy intercepts LLM API traffic, collects token usage, and forwards to providers.
type GatewayProxy struct {
	cfg        Config
	httpClient *http.Client
	providers  map[string]string
	Routes     *RouteManager
	Cache      *ResponseCache
	Quotas     *QuotaLimiter
	trafficMu  sync.RWMutex
	trafficLog []ProxyTrafficRecord
	maxTraffic int
	sessionMu  sync.Mutex
	sessions   map[string]*proxySessionTracker
}

// NewGatewayProxy creates a new GatewayProxy instance with standard provider registries, route manager, cache, and quota limiter.
func NewGatewayProxy(cfg Config) *GatewayProxy {
	providers := map[string]string{
		"openai":     "https://api.openai.com",
		"anthropic":  "https://api.anthropic.com",
		"deepseek":   "https://api.deepseek.com",
		"openrouter": "https://openrouter.ai/api",
		"groq":       "https://api.groq.com/openai",
		"together":   "https://api.together.xyz",
		"fireworks":  "https://api.fireworks.ai/inference",
		"mistral":    "https://api.mistral.ai",
		"ollama":     "http://localhost:11434",
	}

	for k, v := range cfg.CustomUpstreams {
		providers[strings.ToLower(k)] = v
	}

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 120 * time.Second,
	}

	return &GatewayProxy{
		cfg: cfg,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   0, // 0 = no arbitrary timeout killing active SSE streams
		},
		providers:  providers,
		Routes:     NewRouteManager(),
		Cache:      NewResponseCache(2000, 24*time.Hour),
		Quotas:     NewQuotaLimiter(),
		trafficLog: make([]ProxyTrafficRecord, 0, 100),
		maxTraffic: 200,
		sessions:   make(map[string]*proxySessionTracker),
	}
}

// AllTraffic returns recent captured raw proxy traffic logs.
func (p *GatewayProxy) AllTraffic(limit int) []ProxyTrafficRecord {
	p.trafficMu.RLock()
	defer p.trafficMu.RUnlock()
	if limit <= 0 || limit > len(p.trafficLog) {
		limit = len(p.trafficLog)
	}
	out := make([]ProxyTrafficRecord, limit)
	// Return in reverse chronological order (newest first)
	for i := 0; i < limit; i++ {
		out[i] = p.trafficLog[len(p.trafficLog)-1-i]
	}
	return out
}

// ClearTraffic empties the in-memory traffic ring buffer.
func (p *GatewayProxy) ClearTraffic() {
	p.trafficMu.Lock()
	defer p.trafficMu.Unlock()
	p.trafficLog = make([]ProxyTrafficRecord, 0, p.maxTraffic)
}

func (p *GatewayProxy) recordTraffic(rec ProxyTrafficRecord) {
	// Single choke point: every stored/broadcast record is size-capped and
	// partially masked here. Analysis paths upstream ran on the ORIGINAL
	// payloads; only the persisted/broadcast copy is sanitized.
	rec.RequestBody = sanitizeBodyForRecord(rec.RequestBody)
	rec.ResponseBody = sanitizeBodyForRecord(rec.ResponseBody)

	p.trafficMu.Lock()
	if len(p.trafficLog) >= p.maxTraffic {
		p.trafficLog = p.trafficLog[1:]
	}
	p.trafficLog = append(p.trafficLog, rec)
	p.trafficMu.Unlock()

	if p.cfg.OnTraffic != nil {
		p.cfg.OnTraffic(rec)
	}
}

// stripProxyMountLabel removes the "/proxy/<label>" addressing prefix from
// an incoming proxy path. The label is a human-friendly route tag, not part
// of the upstream API path. Paths mounted at /v1 (no /proxy prefix) and
// embedded host forms (/proxy/host:port/..., /proxy/https://...) are
// returned unchanged — their first segment IS addressing, not a label.
func stripProxyMountLabel(path string) string {
	if !strings.HasPrefix(path, "/proxy/") {
		return path
	}
	rest := strings.TrimPrefix(path, "/proxy/") // "mock/v1/chat/completions"
	label, remain := rest, ""
	if slash := strings.Index(rest, "/"); slash != -1 {
		label, remain = rest[:slash], rest[slash:]
	}
	// A label containing ":" or "." is an embedded host/scheme, not a mount
	// label — leave the path untouched.
	if strings.ContainsAny(label, ":.") {
		return path
	}
	if remain == "" {
		return "/"
	}
	return remain
}

// isModelCatalogPath reports whether the resolved upstream path addresses the
// model catalog / metadata API rather than an inference endpoint, e.g. /v1/models
// (list), /v1/models/gpt-4o (retrieve), or Ollama's native tag listing /api/tags.
// Catalog calls carry no model request: no inference, no usage, nothing to
// trace — ServeHTTP relays them transparently.
func isModelCatalogPath(cleanPath string) bool {
	trimmed := strings.Trim(cleanPath, "/")
	if trimmed == "" {
		return false
	}
	segs := strings.Split(trimmed, "/")
	last := strings.ToLower(segs[len(segs)-1])
	if last == "models" {
		return true
	}
	// Detail form: .../models/<id>. Gemini routes inference THROUGH the path as
	// models/<model>:generateContent — the ":" method suffix distinguishes
	// those, so they stay on the traced model-request path.
	if len(segs) >= 2 && strings.ToLower(segs[len(segs)-2]) == "models" && !strings.Contains(last, ":") {
		return true
	}
	// Ollama native tag listing: <base>/api/tags (no version prefix).
	if last == "tags" && len(segs) >= 2 && strings.ToLower(segs[len(segs)-2]) == "api" {
		return true
	}
	return false
}

// DetectProvider resolves the exact provider using configured dynamic routes, headers, URL path, or API key signature.
func (p *GatewayProxy) DetectProvider(r *http.Request) (provider string, targetBaseURL string, cleanPath string) {
	path := r.URL.Path
	customUpstream := r.Header.Get("X-Target-Upstream")
	if customUpstream == "" {
		customUpstream = r.Header.Get("X-Upstream-Base")
	}

	if customUpstream != "" {
		customProvider := r.Header.Get("X-Provider-Name")
		if customProvider == "" {
			customProvider = "CustomUpstream"
		}
		// The header addresses the upstream directly, so the incoming path's
		// /proxy/<label> addressing prefix must not leak into the forwarded
		// URL (was: base /v1 + /proxy/mock/v1/chat/completions →
		// …/v1/proxy/mock/v1/chat/completions → 404). Strip the mount label;
		// ServeHTTP's existing v1-dedup then collapses any remaining overlap.
		return customProvider, customUpstream, stripProxyMountLabel(path)
	}

	// 1. Check user-configured dynamic routes first
	if p.Routes != nil {
		if matchedRoute, remaining := p.Routes.MatchRoute(path); matchedRoute != nil {
			return matchedRoute.Name, matchedRoute.TargetUpstream, remaining
		}
	}

	// 2. Direct Embedded Full URL / Hostname Passthrough:
	// e.g. /proxy/api.z.ai/api/coding/paas/v4/chat/completions
	// e.g. /proxy/https://api.z.ai/api/coding/paas/v4/chat/completions
	// e.g. /proxy/api.groq.com/openai/v1/chat/completions
	// e.g. /proxy/localhost:11434/v1/chat/completions
	trimmedPath := strings.TrimPrefix(path, "/")
	if strings.HasPrefix(trimmedPath, "proxy/") {
		sub := strings.TrimPrefix(trimmedPath, "proxy/")

		// Check if URL has embedded scheme: /proxy/https://... or /proxy/https:/... or /proxy/http://...
		if strings.HasPrefix(sub, "https:/") || strings.HasPrefix(sub, "http:/") {
			var scheme string
			var rest string
			if strings.HasPrefix(sub, "https://") {
				scheme = "https://"
				rest = strings.TrimPrefix(sub, "https://")
			} else if strings.HasPrefix(sub, "https:/") {
				scheme = "https://"
				rest = strings.TrimPrefix(sub, "https:/")
			} else if strings.HasPrefix(sub, "http://") {
				scheme = "http://"
				rest = strings.TrimPrefix(sub, "http://")
			} else if strings.HasPrefix(sub, "http:/") {
				scheme = "http://"
				rest = strings.TrimPrefix(sub, "http:/")
			}

			parts := strings.SplitN(rest, "/", 2)
			host := parts[0]
			rem := "/"
			if len(parts) > 1 {
				rem = "/" + parts[1]
			}
			return prettifyProvider(host), scheme + host, rem
		}

		// Check if first segment is a hostname, domain, or IP (contains '.' or ':')
		parts := strings.SplitN(sub, "/", 2)
		target := parts[0]
		if strings.Contains(target, ".") || strings.Contains(target, ":") {
			scheme := "https://"
			if strings.HasPrefix(target, "localhost") || strings.HasPrefix(target, "127.0.0.1") || strings.HasPrefix(target, "0.0.0.0") {
				scheme = "http://"
			}
			rem := "/"
			if len(parts) > 1 {
				rem = "/" + parts[1]
			}
			return prettifyProvider(target), scheme + target, rem
		}

		// Check named provider alias in standard providers map
		if base, ok := p.providers[strings.ToLower(target)]; ok {
			rem := "/"
			if len(parts) > 1 {
				rem = "/" + parts[1]
			}
			return prettifyProvider(target), base, rem
		}
	}

	// 3. Registered providers from CustomUpstreams
	for k, v := range p.providers {
		if strings.Contains(strings.ToLower(path), k) {
			return strings.Title(k), v, path
		}
	}

	// 4. Fallback to default route if configured
	if def, ok := p.providers["default"]; ok && def != "" {
		return "Default", def, path
	}

	return "", "", path
}

func prettifyProvider(host string) string {
	lower := strings.ToLower(host)
	switch {
	case strings.Contains(lower, "z.ai") || strings.Contains(lower, "zhipu") || strings.Contains(lower, "bigmodel"):
		return "Z.AI"
	case strings.Contains(lower, "groq"):
		return "Groq"
	case strings.Contains(lower, "deepseek"):
		return "DeepSeek"
	case strings.Contains(lower, "openai"):
		return "OpenAI"
	case strings.Contains(lower, "anthropic") || strings.Contains(lower, "claude"):
		return "Anthropic"
	case strings.Contains(lower, "openrouter"):
		return "OpenRouter"
	case strings.Contains(lower, "gemini") || strings.Contains(lower, "generativelanguage"):
		return "Gemini"
	case strings.Contains(lower, "ollama") || strings.Contains(lower, "11434"):
		return "Ollama"
	case strings.Contains(lower, "together"):
		return "Together"
	case strings.Contains(lower, "mistral"):
		return "Mistral"
	case strings.Contains(lower, "cohere"):
		return "Cohere"
	case strings.Contains(lower, "fireworks"):
		return "Fireworks"
	case strings.Contains(lower, "vllm"):
		return "vLLM"
	default:
		if host == "" {
			return "Custom"
		}
		parts := strings.Split(host, ".")
		if len(parts) >= 2 {
			if parts[0] == "api" && len(parts) >= 3 && parts[1] != "" {
				return strings.ToUpper(parts[1][:1]) + parts[1][1:]
			}
			if parts[0] == "" {
				return host
			}
			return strings.ToUpper(parts[0][:1]) + parts[0][1:]
		}
		return strings.ToUpper(host[:1]) + host[1:]
	}
}

// ServeHTTP routes LLM calls to the exact resolved provider.
func (p *GatewayProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	provider, targetBase, cleanPath := p.DetectProvider(r)
	if targetBase == "" {
		http.Error(w, "wrongtrace proxy: no upstream configured for path "+r.URL.Path+". Please configure a route in the AI Gateway tab or supply an X-Target-Upstream header.", http.StatusBadRequest)
		return
	}

	if !strings.HasPrefix(cleanPath, "/") && cleanPath != "" {
		cleanPath = "/" + cleanPath
	}

	// If a user navigates to the proxy root in a browser or checks status via GET
	if r.Method == http.MethodGet && (cleanPath == "" || cleanPath == "/") && r.ContentLength <= 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status":   "active",
			"gateway":  "WrongTrace AI Gateway Proxy",
			"provider": provider,
			// Scrub credentials: a custom upstream may embed a query key
			// (X-Target-Upstream is attacker-controllable header input).
			"upstream": sanitizeURLForRecord(targetBase),
			"message":  "WrongTrace proxy is live. Point your LLM clients / agents here for telemetry, token metrics, and cost tracking.",
		})
		return
	}

	cleanPath = strings.TrimPrefix(cleanPath, "/")
	lowerBase := strings.ToLower(targetBase)
	// Avoid duplicate version path (e.g. /paas/v4/v1/chat/completions -> /paas/v4/chat/completions)
	if (strings.HasSuffix(lowerBase, "/v4") || strings.HasSuffix(lowerBase, "/v1") || strings.HasSuffix(lowerBase, "/v1beta") || strings.Contains(lowerBase, "/paas/v4")) && strings.HasPrefix(cleanPath, "v1/") {
		cleanPath = strings.TrimPrefix(cleanPath, "v1/")
	}

	// Model catalog / metadata calls (e.g. GET /v1/models) are not model
	// requests: no inference runs, no usage comes back, and there is nothing
	// to trace. Relay them transparently — no run recording, no traffic
	// record, no quota or cache involvement — so the proxy/trace flow only
	// ever sees real inference calls.
	if isModelCatalogPath(cleanPath) {
		p.relayCatalogRequest(w, r, provider, targetBase, cleanPath)
		return
	}

	// Default POST to chat/completions if path is empty
	if (cleanPath == "" || cleanPath == "/") && r.Method == http.MethodPost {
		cleanPath = "chat/completions"
	}

	targetURL := strings.TrimSuffix(targetBase, "/") + "/" + cleanPath
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}
	// safeTargetURL is the credential-scrubbed form used ONLY for logs and
	// traffic records; the forwarded request keeps the real URL so
	// query-authenticated providers (e.g. Gemini ?key=...) keep working.
	safeTargetURL := sanitizeURLForRecord(targetURL)

	// Read request body to extract model and intent
	reqBody, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "cannot read request body", http.StatusBadRequest)
		return
	}

	// 1. Real-time Secret Scanner: sanitize leaked API keys or passwords before sending to cloud LLMs
	reqBody, redactedSecrets := ScanAndRedactSecrets(reqBody)
	if redactedSecrets > 0 {
		log.Printf("proxy: security guardrail redacted %d secret(s) in outgoing payload to %s", redactedSecrets, provider)
	}
	r.Body = io.NopCloser(bytes.NewReader(reqBody))

	var parsedReq struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(reqBody, &parsedReq)

	modelName := parsedReq.Model
	if modelName == "" {
		modelName = "unknown-model"
	}

	var userIntent string
	for i := len(parsedReq.Messages) - 1; i >= 0; i-- {
		if parsedReq.Messages[i].Role == "user" {
			userIntent = runeSafePrefix(parsedReq.Messages[i].Content, 80)
			if len(parsedReq.Messages[i].Content) > len(userIntent) {
				userIntent += "…"
			}
			break
		}
	}

	start := time.Now()
	reqHeaders := maskHeaders(r.Header)

	// Extract contextual tracing headers
	projectID := r.Header.Get("X-Project-ID")
	projectSlug := r.Header.Get("X-Project-Slug")
	agentName := r.Header.Get("X-Agent-Name")
	taskID := r.Header.Get("X-Task-ID")
	if taskID == "" {
		taskID = "ProxyGateway"
	}
	if agentName == "" {
		if projectSlug != "" || projectID != "" {
			agentName = "WrongStack"
		} else {
			agentName = "AI-Proxy"
		}
	}

	sessionID := r.Header.Get("X-Session-ID")
	if sessionID == "" {
		sessionID = r.Header.Get("X-Run-ID")
	}
	if sessionID == "" {
		sessionID = r.Header.Get("X-WrongTrace-Session")
	}
	if sessionID == "" {
		sessionID = r.Header.Get("X-Conversation-ID")
	}

	clientIP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		clientIP = r.RemoteAddr
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		clientIP = strings.TrimSpace(strings.Split(fwd, ",")[0])
	}

	sessionKey := fmt.Sprintf("%s|%s|%s|%s", clientIP, agentName, projectSlug, modelName)

	// 2. Token Budget & Cost Quota Guardrail Check
	quotaKey := projectSlug
	if quotaKey == "" {
		quotaKey = agentName
	}
	if allowed, _, quotaMsg := p.Quotas.CheckAndRecordSpend(quotaKey, 0.0); !allowed {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{
				"message": quotaMsg,
				"type":    "quota_exceeded_error",
				"code":    429,
			},
		})
		return
	}

	// 3. Exact Response Cache Lookup
	bypassCache := r.Header.Get("Cache-Control") == "no-cache" || r.Header.Get("X-Bypass-Cache") == "true"
	cacheKey := ComputeKey(provider, modelName, reqBody)

	if !bypassCache && p.Cache != nil {
		if cached, hit := p.Cache.Get(cacheKey); hit {
			for k, v := range cached.Headers {
				w.Header().Set(k, v)
			}
			w.Header().Set("X-WrongTrace-Cache", "HIT")
			w.WriteHeader(cached.StatusCode)
			_, _ = w.Write(cached.Body)

			rec := ProxyTrafficRecord{
				ID:              randomID("cache_hit"),
				Timestamp:       start,
				DurationMs:      time.Since(start).Milliseconds(),
				Method:          r.Method,
				IncomingPath:    r.URL.Path,
				TargetURL:       safeTargetURL,
				Provider:        provider,
				Model:           modelName,
				AgentName:       agentName,
				TaskID:          taskID,
				RunID:           sessionID,
				SessionKey:      sessionKey,
				ProjectID:       projectID,
				ProjectSlug:     projectSlug,
				StatusCode:      cached.StatusCode,
				IsStream:        cached.IsStream,
				RequestHeaders:  reqHeaders,
				RequestBody:     string(reqBody),
				ResponseHeaders: cached.Headers,
				ResponseBody:    string(cached.Body),
				PromptTokens:    cached.TokensSaved,
				TotalTokens:     cached.TokensSaved,
				CachedTokens:    cached.TokensSaved,
				CacheHitRate:    100.0,
				CostUSD:         0.0, // $0 cost because served from exact cache!
				CacheSavingsUSD: cached.CostSavedUSD,
				AssistantReply:  userIntent,
			}
			p.recordTraffic(rec)
			return
		}
	}

	// Automatically inject stream_options.include_usage for OpenAI-compatible streaming if missing
	isStream := parsedReq.Stream || strings.Contains(r.Header.Get("Accept"), "text/event-stream")
	if isStream && bytes.Contains(reqBody, []byte(`"stream"`)) && !bytes.Contains(reqBody, []byte(`"stream_options"`)) {
		var reqMap map[string]interface{}
		if err := json.Unmarshal(reqBody, &reqMap); err == nil {
			reqMap["stream_options"] = map[string]interface{}{
				"include_usage": true,
			}
			if modified, err := json.Marshal(reqMap); err == nil {
				reqBody = modified
			}
		}
	}

	// Build outgoing proxy request
	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, bytes.NewReader(reqBody))
	if err != nil {
		http.Error(w, "create proxy request failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Copy headers (strip Host, Content-Length, hop-by-hop, and internal tracing headers)
	copyProxyHeaders(outReq, r.Header)
	outReq.Host = outReq.URL.Host

	log.Printf("proxy: forwarding %s %s -> %s (provider: %s, model: %s)", r.Method, r.URL.Path, safeTargetURL, provider, modelName)

	resp, err := p.httpClient.Do(outReq)
	if err != nil {
		duration := time.Since(start).Milliseconds()
		// err.Error() embeds the full request URL, which can carry the API
		// key in its query (?key=...). Scrub it before logging, echoing to
		// the client, or persisting to the traffic log. The regex scrub is
		// form-independent: http.Client re-normalizes the URL inside the
		// error, so an exact ReplaceAll(targetURL→safeURL) can miss it.
		safeErr := scrubErrorString(err.Error())
		log.Printf("proxy: upstream error for %s: %s", safeTargetURL, safeErr)
		http.Error(w, "upstream error: "+safeErr, http.StatusBadGateway)

		// Record error traffic
		p.recordTraffic(ProxyTrafficRecord{
			ID:             randomID("traffic"),
			Timestamp:      start,
			DurationMs:     duration,
			Method:         r.Method,
			IncomingPath:   r.URL.Path,
			TargetURL:      safeTargetURL,
			Provider:       provider,
			Model:          modelName,
			StatusCode:     http.StatusBadGateway,
			RequestHeaders: reqHeaders,
			RequestBody:    string(reqBody),
			ResponseBody:   `{"error": "` + safeErr + `"}`,
		})
		return
	}
	defer resp.Body.Close()

	duration := time.Since(start).Milliseconds()
	log.Printf("proxy: %s responded with HTTP %d in %dms", safeTargetURL, resp.StatusCode, duration)

	isStream = parsedReq.Stream || strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")

	// Copy response headers (omit Content-Length for streaming SSE responses)
	for k, vv := range resp.Header {
		lowerK := strings.ToLower(k)
		if isStream && (lowerK == "content-length" || lowerK == "transfer-encoding") {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	if isStream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
	}
	w.WriteHeader(resp.StatusCode)
	respHeaders := maskHeaders(resp.Header)

	baseRecord := ProxyTrafficRecord{
		ID:             randomID("traffic"),
		Timestamp:      start,
		DurationMs:     duration,
		Method:         r.Method,
		IncomingPath:   r.URL.Path,
		TargetURL:      safeTargetURL,
		Provider:       provider,
		Model:          modelName,
		AgentName:      agentName,
		TaskID:         taskID,
		RunID:          sessionID,
		SessionKey:     sessionKey,
		ProjectID:      projectID,
		ProjectSlug:    projectSlug,
		StatusCode:     resp.StatusCode,
		RequestHeaders: reqHeaders,
		// Raw on purpose: analysis in the handlers runs on rec.RequestBody/
		// respBytes; recordTraffic() is the single sanitizing choke point.
		RequestBody:     string(reqBody),
		ResponseHeaders: respHeaders,
	}

	baseRecord.IsStream = isStream

	if isStream {
		p.handleStreamingResponse(w, resp.Body, baseRecord, userIntent)
	} else {
		p.handleJSONResponse(w, resp.Body, baseRecord, userIntent)
	}
}

// relayCatalogRequest transparently forwards a model-catalog / metadata call
// (e.g. GET /v1/models, GET /v1/models/<id>) to the resolved upstream and
// streams the answer back to the client. Catalog calls are not model requests
// — nothing is inferred, no usage is returned, no tokens or cost exist — so
// they must never enter the proxy/trace flow: no run recording, no traffic
// record, no cache or quota interaction.
func (p *GatewayProxy) relayCatalogRequest(w http.ResponseWriter, r *http.Request, provider, targetBase, cleanPath string) {
	targetURL := strings.TrimSuffix(targetBase, "/") + "/" + strings.TrimPrefix(cleanPath, "/")
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, r.Body)
	if err != nil {
		http.Error(w, "create proxy request failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	copyProxyHeaders(outReq, r.Header)
	outReq.Host = outReq.URL.Host

	log.Printf("proxy: relaying catalog call %s %s -> %s (provider: %s, untraced)", r.Method, r.URL.Path, sanitizeURLForRecord(targetURL), provider)

	resp, err := p.httpClient.Do(outReq)
	if err != nil {
		safeErr := scrubErrorString(err.Error())
		log.Printf("proxy: upstream error for %s: %s", sanitizeURLForRecord(targetURL), safeErr)
		http.Error(w, "upstream error: "+safeErr, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (p *GatewayProxy) handleJSONResponse(w http.ResponseWriter, body io.Reader, rec ProxyTrafficRecord, intent string) {
	respBytes, err := io.ReadAll(body)
	if err != nil {
		return
	}
	_, _ = w.Write(respBytes)

	var parsedResp struct {
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
			InputTokens      int64 `json:"input_tokens"`
			OutputTokens     int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(respBytes, &parsedResp)

	promptTokens := parsedResp.Usage.PromptTokens
	if promptTokens == 0 {
		promptTokens = parsedResp.Usage.InputTokens
	}
	completionTokens := parsedResp.Usage.CompletionTokens
	if completionTokens == 0 {
		completionTokens = parsedResp.Usage.OutputTokens
	}

	rec.ResponseBody = string(respBytes)

	// Perform deep wire traffic analysis (tool calls, reasoning, cached tokens, replies)
	// on the ORIGINAL bytes; recordTraffic() sanitizes only the stored copy.
	analysis := AnalyzeWirePayloads([]byte(rec.RequestBody), respBytes, false)
	if analysis.PromptTokens > 0 {
		promptTokens = analysis.PromptTokens
	}
	if analysis.CompletionTokens > 0 {
		completionTokens = analysis.CompletionTokens
	}
	if analysis.WireModel != "" && (rec.Model == "" || rec.Model == "unknown-model") {
		rec.Model = analysis.WireModel
	}
	if analysis.WireID != "" {
		rec.ID = analysis.WireID
	}

	if analysis.CachedTokens > 0 && promptTokens < analysis.CachedTokens {
		promptTokens += analysis.CachedTokens
	}

	rec.PromptTokens = promptTokens
	rec.CompletionTokens = completionTokens
	rec.TotalTokens = promptTokens + completionTokens
	rec.CachedTokens = analysis.CachedTokens
	rec.ReasoningTokens = analysis.ReasoningTokens
	if promptTokens > 0 && analysis.CachedTokens > 0 {
		rate := (float64(analysis.CachedTokens) / float64(promptTokens)) * 100
		if rate > 100.0 {
			rate = 100.0
		}
		rec.CacheHitRate = rate
	}

	costUSD, cacheSavingsUSD := models.Global.CalculateCostDetailed(rec.Provider, rec.Model, promptTokens, completionTokens, analysis.CachedTokens)
	rec.CostUSD = costUSD
	rec.CacheSavingsUSD = cacheSavingsUSD

	rec.ToolCalls = analysis.ToolCalls
	rec.ToolCount = analysis.ToolCount
	rec.AssistantReply = analysis.AssistantReply
	rec.Reasoning = analysis.Reasoning
	rec.SystemPrompt = analysis.SystemPrompt
	rec.MessageCount = analysis.MessageCount
	rec.FinishReason = analysis.FinishReason

	if intent == "" && analysis.AssistantReply != "" {
		intent = runeSafePrefix(analysis.AssistantReply, 80)
		if len(analysis.AssistantReply) > len(intent) {
			intent += "…"
		}
	}

	runID := p.recordRun(rec.Model, rec.Provider, rec.AgentName, rec.TaskID, rec.ProjectID, rec.ProjectSlug, rec.RunID, rec.SessionKey, promptTokens, completionTokens, rec.CostUSD, intent)
	if runID != "" {
		rec.RunID = runID
	}
	p.recordTraffic(rec)

	if p.Quotas != nil && rec.CostUSD > 0 {
		quotaKey := rec.ProjectSlug
		if quotaKey == "" {
			quotaKey = rec.AgentName
		}
		p.Quotas.RecordSpend(quotaKey, rec.CostUSD)
	}

	// Save to response cache if successful
	if rec.StatusCode == http.StatusOK && p.Cache != nil && len(respBytes) > 0 {
		cacheKey := ComputeKey(rec.Provider, rec.Model, []byte(rec.RequestBody))
		p.Cache.Set(cacheKey, rec.Provider, rec.Model, rec.StatusCode, rec.ResponseHeaders, respBytes, false, promptTokens+completionTokens, costUSD, 24*time.Hour)
	}
}

func (p *GatewayProxy) handleStreamingResponse(w http.ResponseWriter, body io.Reader, rec ProxyTrafficRecord, intent string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, isFlusher := w.(http.Flusher)
	buf := make([]byte, 4096)

	var promptTokens, completionTokens int64
	var capturedBuffer strings.Builder

	for {
		n, err := body.Read(buf)
		if n > 0 {
			_, _ = w.Write(buf[:n])
			if isFlusher {
				flusher.Flush()
			}
			capturedBuffer.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}

	fullSSE := capturedBuffer.String()

	// Guarantee terminal marker: if upstream closed stream cleanly without [DONE], emit terminal marker
	if strings.Contains(fullSSE, "data:") && !strings.Contains(fullSSE, "[DONE]") {
		terminalMarker := []byte("data: [DONE]\n\n")
		_, _ = w.Write(terminalMarker)
		if isFlusher {
			flusher.Flush()
		}
		capturedBuffer.Write(terminalMarker)
		fullSSE = capturedBuffer.String()
	}

	// Parse SSE usage chunks if available
	if strings.Contains(fullSSE, "usage") {
		var sseObj struct {
			Usage struct {
				PromptTokens     int64 `json:"prompt_tokens"`
				CompletionTokens int64 `json:"completion_tokens"`
			} `json:"usage"`
		}
		_ = json.Unmarshal([]byte(fullSSE), &sseObj)
		promptTokens = sseObj.Usage.PromptTokens
		completionTokens = sseObj.Usage.CompletionTokens
	}

	// Perform deep wire traffic analysis on streamed response (SSE chunks + usage metadata)
	analysis := AnalyzeWirePayloads([]byte(rec.RequestBody), []byte(fullSSE), true)
	if analysis.PromptTokens > 0 {
		promptTokens = analysis.PromptTokens
	}
	if analysis.CompletionTokens > 0 {
		completionTokens = analysis.CompletionTokens
	}
	if analysis.WireModel != "" && (rec.Model == "" || rec.Model == "unknown-model") {
		rec.Model = analysis.WireModel
	}
	if analysis.WireID != "" {
		rec.ID = analysis.WireID
	}

	if promptTokens == 0 {
		promptTokens = EstimatePromptTokens([]byte(rec.RequestBody))
	}
	if completionTokens == 0 {
		if analysis.CompletionTokens > 0 {
			completionTokens = analysis.CompletionTokens
		} else {
			completionTokens = 1
		}
	}

	if analysis.CachedTokens > 0 && promptTokens < analysis.CachedTokens {
		promptTokens += analysis.CachedTokens
	}

	rec.ResponseBody = fullSSE
	rec.PromptTokens = promptTokens
	rec.CompletionTokens = completionTokens
	rec.TotalTokens = promptTokens + completionTokens
	rec.CachedTokens = analysis.CachedTokens
	rec.ReasoningTokens = analysis.ReasoningTokens
	if promptTokens > 0 && analysis.CachedTokens > 0 {
		rate := (float64(analysis.CachedTokens) / float64(promptTokens)) * 100
		if rate > 100.0 {
			rate = 100.0
		}
		rec.CacheHitRate = rate
	}

	costUSD, cacheSavingsUSD := models.Global.CalculateCostDetailed(rec.Provider, rec.Model, promptTokens, completionTokens, analysis.CachedTokens)
	rec.CostUSD = costUSD
	rec.CacheSavingsUSD = cacheSavingsUSD

	rec.ToolCalls = analysis.ToolCalls
	rec.ToolCount = analysis.ToolCount
	rec.AssistantReply = analysis.AssistantReply
	rec.Reasoning = analysis.Reasoning
	rec.SystemPrompt = analysis.SystemPrompt
	rec.MessageCount = analysis.MessageCount
	rec.FinishReason = analysis.FinishReason

	if intent == "" && analysis.AssistantReply != "" {
		intent = runeSafePrefix(analysis.AssistantReply, 80)
		if len(analysis.AssistantReply) > len(intent) {
			intent += "…"
		}
	}

	runID := p.recordRun(rec.Model, rec.Provider, rec.AgentName, rec.TaskID, rec.ProjectID, rec.ProjectSlug, rec.RunID, rec.SessionKey, promptTokens, completionTokens, rec.CostUSD, intent)
	if runID != "" {
		rec.RunID = runID
	}
	p.recordTraffic(rec)

	if p.Quotas != nil && rec.CostUSD > 0 {
		quotaKey := rec.ProjectSlug
		if quotaKey == "" {
			quotaKey = rec.AgentName
		}
		p.Quotas.RecordSpend(quotaKey, rec.CostUSD)
	}

	// Save to response cache if successful
	if rec.StatusCode == http.StatusOK && p.Cache != nil && len(fullSSE) > 0 {
		cacheKey := ComputeKey(rec.Provider, rec.Model, []byte(rec.RequestBody))
		p.Cache.Set(cacheKey, rec.Provider, rec.Model, rec.StatusCode, rec.ResponseHeaders, []byte(fullSSE), true, promptTokens+completionTokens, costUSD, 24*time.Hour)
	}
}

func maskHeaders(h http.Header) map[string]string {
	out := make(map[string]string)
	for k, vv := range h {
		// Mask each header value individually: joining first would corrupt
		// multi-value headers (e.g. several Set-Cookie lines) and could let
		// a crafted value escape redaction inside the joined blob.
		masked := make([]string, len(vv))
		for i, v := range vv {
			masked[i] = maskSecretValue(k, v)
		}
		out[k] = strings.Join(masked, ", ")
	}
	return out
}

// maskSecretValue redacts credential-bearing header values, keeping a short
// prefix/suffix so operators can still identify which key was used.
func maskSecretValue(k, val string) string {
	lowerK := strings.ToLower(k)
	switch lowerK {
	case "authorization", "x-api-key", "api-key", "x-goog-api-key",
		"proxy-authorization", "cookie", "set-cookie", "x-auth-token", "x-session-token",
		"x-amz-security-token", "private-token", "gitlab-token":
		// Case-insensitive "Bearer " prefix (clients may send "bearer ...").
		if len(val) > 7 && strings.EqualFold(val[:7], "Bearer ") {
			token := val[7:]
			if len(token) > 10 {
				return val[:7] + token[:6] + "..." + token[len(token)-4:]
			}
			return val[:7] + "***"
		}
		if len(val) > 10 {
			return val[:4] + "..." + val[len(val)-4:]
		}
		return "***"
	}
	return val
}

// credQueryParamRe matches credential-looking query parameters inside an
// arbitrary string (error messages, URLs). Single character class with no
// nested quantifiers — linear time, no ReDoS surface.
var credQueryParamRe = regexp.MustCompile(`(?i)((?:key|api[-_]?key|token|access[-_]token|refresh[-_]token|signature|secret|client_secret|password)\s*=\s*)[^&"'\s\\]+`)

// scrubErrorString redacts credential query values from an error string.
// http.Client errors embed the request URL in re-normalized form (encoding,
// casing, path collapsing can differ from the literal target URL), so exact
// string replacement would miss variants; this scrub is form-independent.
func scrubErrorString(s string) string {
	return credQueryParamRe.ReplaceAllString(s, "$1[redacted]")
}

// Body-redaction limits for traffic records. Individual JSON string values
// (prompt/completion content) are truncated head+tail so the inspector still
// shows structure and beginnings; the whole stored body is capped.
const (
	maxRecordStringLen = 512       // per JSON string value: 384 head + 128 tail
	maxRecordBodyLen   = 64 * 1024 // total stored body cap (JSON or SSE)
)

// sanitizeBodyForRecord returns a size-capped, partially-masked copy of a
// wire body for ProxyTrafficRecord storage. JSON bodies are walked: string
// values longer than maxRecordStringLen are head/tail-truncated and values
// under credential-looking keys are fully redacted, keeping the output valid
// JSON for the dashboard inspector. Non-JSON bodies (SSE streams, plain
// text) fall back to per-line masking plus the credential regex scrub.
// Analysis paths always run on the ORIGINAL payload — only the stored copy
// is sanitized.
func sanitizeBodyForRecord(body string) string {
	if body == "" {
		return body
	}

	var out string
	trimmed := strings.TrimSpace(body)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var v interface{}
		if err := json.Unmarshal([]byte(trimmed), &v); err == nil {
			if b, err := json.Marshal(maskJSONValue(v)); err == nil {
				out = string(b)
			}
		}
	}
	if out == "" {
		out = maskNonJSONBody(body)
	}

	if len(out) > maxRecordBodyLen {
		head := runeSafePrefix(out, maxRecordBodyLen*3/4)
		tail := runeSafeSuffix(out, maxRecordBodyLen/4)
		truncated := len(out) - len(head) - len(tail)
		out = head + "\n…[body truncated " + strconv.Itoa(truncated) + " chars]…\n" + tail
	}
	return out
}

// maskJSONValue walks a decoded JSON value, truncating long strings and
// redacting credential-keyed values. Numbers/bools pass through unchanged.
func maskJSONValue(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, val := range t {
			if isCredentialKey(k) {
				out[k] = "[redacted]"
				continue
			}
			out[k] = maskJSONValue(val)
		}
		return out
	case []interface{}:
		arr := make([]interface{}, len(t))
		for i, val := range t {
			arr[i] = maskJSONValue(val)
		}
		return arr
	case string:
		return truncateRecordString(t)
	default:
		return v
	}
}

// maskNonJSONBody handles SSE / plain-text bodies: each `data:`-prefixed
// JSON chunk is walked like a JSON body (per-chunk masking keeps the stream
// structure intact); other lines get the credential regex scrub.
func maskNonJSONBody(body string) string {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
			if payload != "" && payload != "[DONE]" &&
				(strings.HasPrefix(payload, "{") || strings.HasPrefix(payload, "[")) {
				var v interface{}
				if err := json.Unmarshal([]byte(payload), &v); err == nil {
					if b, err := json.Marshal(maskJSONValue(v)); err == nil {
						lines[i] = "data: " + string(b)
						continue
					}
				}
			}
		}
		lines[i] = scrubErrorString(line)
	}
	return strings.Join(lines, "\n")
}

// truncateRecordString keeps the head (3/4) and tail (1/4) of an overly long
// string with an explicit marker, so the inspector shows structure and where
// content began/ended without storing the whole prompt.
func truncateRecordString(s string) string {
	if len(s) <= maxRecordStringLen {
		return s
	}
	head := runeSafePrefix(s, maxRecordStringLen*3/4)
	tail := runeSafeSuffix(s, maxRecordStringLen/4)
	return head + "…[+" + strconv.Itoa(len(s)-len(head)-len(tail)) + " chars]…" + tail
}

func isCredentialKey(k string) bool {
	lk := strings.ToLower(strings.TrimSpace(k))
	// Usage/token-count fields look tokenish but are plain metadata — never
	// redact them (E2E caught prompt_tokens being masked as [redacted]).
	switch lk {
	case "tokens", "prompt_tokens", "completion_tokens", "total_tokens",
		"max_tokens", "max_completion_tokens", "reasoning_tokens",
		"cached_tokens", "input_tokens", "output_tokens", "token_count":
		return false
	}
	switch lk {
	case "authorization", "api_key", "apikey", "api-key", "password", "secret",
		"client_secret", "token", "access_token", "refresh_token", "id_token",
		"session", "key", "credentials", "private_key", "x-api-key":
		return true
	}
	// Suffix "_token" (singular) matches credential keys (access_token,
	// session_token) without catching plural usage counts (*_tokens).
	return strings.HasSuffix(lk, "_token") || strings.Contains(lk, "secret") ||
		strings.Contains(lk, "password") || strings.Contains(lk, "credential") ||
		strings.Contains(lk, "api_key") || strings.Contains(lk, "apikey")
}

// runeSafePrefix/Suffix clamp byte offsets to UTF-8 rune boundaries so
// truncation never emits invalid UTF-8 into JSON output.
func runeSafePrefix(s string, max int) string {
	if max >= len(s) {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

func runeSafeSuffix(s string, max int) string {
	if max >= len(s) {
		return s
	}
	start := len(s) - max
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

// sanitizeURLForRecord scrubs credential-looking query parameters (key, apikey,
// api_key, token, access_token, signature…) from a URL string for logging and
// traffic records. Parsing failures return the raw path without query.
func sanitizeURLForRecord(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		// Fall back to a path-only form rather than leaking the query.
		if idx := strings.Index(rawURL, "?"); idx != -1 {
			return rawURL[:idx] + "?[redacted]"
		}
		return rawURL
	}
	if u.RawQuery == "" {
		return rawURL
	}
	q := u.Query()
	changed := false
	for k := range q {
		lk := strings.ToLower(k)
		if lk == "key" || lk == "apikey" || lk == "api_key" || lk == "token" ||
			lk == "access_token" || lk == "access-token" || lk == "signature" ||
			lk == "secret" || strings.Contains(lk, "password") {
			q.Set(k, "[redacted]")
			changed = true
		}
	}
	if !changed {
		return rawURL
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func (p *GatewayProxy) getOrCreateSession(explicitSessionID, sessionKey string, promptTokens, completionTokens int64, costUSD float64) (string, int64, int64, float64) {
	p.sessionMu.Lock()
	defer p.sessionMu.Unlock()

	if p.sessions == nil {
		p.sessions = make(map[string]*proxySessionTracker)
	}

	now := time.Now()
	// Periodic cleanup of sessions inactive for more than 15 minutes
	if len(p.sessions) > 50 {
		for k, s := range p.sessions {
			if now.Sub(s.lastSeen) > 15*time.Minute {
				delete(p.sessions, k)
			}
		}
	}

	lookupKey := explicitSessionID
	if lookupKey == "" {
		lookupKey = sessionKey
	}

	if sess, ok := p.sessions[lookupKey]; ok && now.Sub(sess.lastSeen) <= 10*time.Minute {
		sess.promptTokens += promptTokens
		sess.completionTokens += completionTokens
		sess.costUSD += costUSD
		sess.lastSeen = now
		return sess.runID, sess.promptTokens, sess.completionTokens, sess.costUSD
	}

	runID := explicitSessionID
	if runID == "" {
		runID = randomID("proxy-run")
	}

	p.sessions[lookupKey] = &proxySessionTracker{
		runID:            runID,
		promptTokens:     promptTokens,
		completionTokens: completionTokens,
		costUSD:          costUSD,
		lastSeen:         now,
		createdAt:        now,
	}

	return runID, promptTokens, completionTokens, costUSD
}

func (p *GatewayProxy) recordRun(modelName, provider, agentName, taskID, projectID, projectSlug, explicitRunID, sessionKey string, promptTokens, completionTokens int64, costUSD float64, intent string) string {
	if p.cfg.Reporter == nil {
		return ""
	}

	if costUSD <= 0 {
		costUSD = models.Global.CalculateCostWithProvider(provider, modelName, promptTokens, completionTokens)
	}
	runID, totalPrompt, totalCompletion, totalCost := p.getOrCreateSession(explicitRunID, sessionKey, promptTokens, completionTokens, costUSD)

	_ = p.cfg.Reporter.ReportRun(ipc.TelemetryReport{
		RunID:            runID,
		TaskID:           taskID,
		ProjectID:        projectID,
		ProjectSlug:      projectSlug,
		AgentName:        agentName,
		ModelName:        modelName,
		Provider:         provider,
		PromptTokens:     totalPrompt,
		CompletionTokens: totalCompletion,
		CostUSD:          totalCost,
		Intent:           intent,
	})

	return runID
}

// copyProxyHeaders forwards client headers onto the outgoing upstream request,
// stripping hop-by-hop headers and WrongTrace-internal routing/tracing headers
// (X-Target-Upstream, X-Project-*, X-Agent-*, …) that must never leak to a
// provider endpoint.
func copyProxyHeaders(dst *http.Request, src http.Header) {
	for k, vv := range src {
		lowerK := strings.ToLower(k)
		switch lowerK {
		case "host", "content-length", "connection", "keep-alive", "proxy-authenticate",
			"proxy-authorization", "te", "trailers", "transfer-encoding", "upgrade", "accept-encoding":
			continue
		}
		if strings.HasPrefix(lowerK, "x-wrongtrace-") ||
			strings.HasPrefix(lowerK, "x-project-") || strings.HasPrefix(lowerK, "x-agent-") ||
			strings.HasPrefix(lowerK, "x-task-") || strings.HasPrefix(lowerK, "x-target-") ||
			strings.HasPrefix(lowerK, "x-upstream-") || strings.HasPrefix(lowerK, "x-provider-") {
			continue
		}
		for _, v := range vv {
			dst.Header.Add(k, v)
		}
	}
}

func randomID(prefix string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%s", prefix, hex.EncodeToString(b))
}
