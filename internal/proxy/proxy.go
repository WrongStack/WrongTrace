package proxy

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wrongstack/wrongtrace/internal/ipc"
	"github.com/wrongstack/wrongtrace/internal/models"
)

// Reporter abstracts the engine method needed to record telemetry.
type Reporter interface {
	ReportRun(p ipc.TelemetryReport) error
}

// Config configures the transparent LLM gateway proxy.
type Config struct {
	Reporter Reporter
	CustomUpstreams map[string]string // providerName -> baseURL
}

// GatewayProxy intercepts LLM API traffic, collects token usage, and forwards to providers.
type GatewayProxy struct {
	cfg        Config
	httpClient *http.Client
	providers  map[string]string
	Routes     *RouteManager
}

// NewGatewayProxy creates a new GatewayProxy instance with standard provider registries and route manager.
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

	return &GatewayProxy{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 180 * time.Second},
		providers:  providers,
		Routes:     NewRouteManager(),
	}
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
		return customProvider, customUpstream, path
	}

	// 1. Check user-configured dynamic routes first
	if matchedRoute, remaining := p.Routes.MatchRoute(path); matchedRoute != nil {
		return matchedRoute.Name, matchedRoute.TargetUpstream, remaining
	}

	cleanPath = path

	// 2. Explicit provider or host subpaths (/proxy/<provider>/... or /proxy/<hostname>/...)
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) >= 2 && parts[0] == "proxy" {
		target := parts[1]
		remaining := "/" + strings.Join(parts[2:], "/")
		if base, ok := p.providers[strings.ToLower(target)]; ok {
			return strings.Title(target), base, remaining
		}
		// If target is a hostname or IP (e.g. localhost:11434, api.custom.com)
		if strings.Contains(target, ".") || strings.Contains(target, ":") {
			scheme := "https://"
			if strings.HasPrefix(target, "localhost") || strings.HasPrefix(target, "127.0.0.1") {
				scheme = "http://"
			}
			return target, scheme + target, remaining
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

// ServeHTTP routes LLM calls to the exact resolved provider.
func (p *GatewayProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	provider, targetBase, cleanPath := p.DetectProvider(r)
	if targetBase == "" {
		http.Error(w, "wrongtrace proxy: no upstream configured for path "+r.URL.Path+". Please configure a route in the AI Gateway tab or supply an X-Target-Upstream header.", http.StatusBadRequest)
		return
	}
	targetURL := strings.TrimSuffix(targetBase, "/") + cleanPath

	// Read request body to extract model and intent
	reqBody, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "cannot read request body", http.StatusBadRequest)
		return
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
			userIntent = parsedReq.Messages[i].Content
			if len(userIntent) > 80 {
				userIntent = userIntent[:80] + "…"
			}
			break
		}
	}

	// Build outgoing proxy request
	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, bytes.NewReader(reqBody))
	if err != nil {
		http.Error(w, "create proxy request failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Copy headers
	for k, vv := range r.Header {
		for _, v := range vv {
			outReq.Header.Add(k, v)
		}
	}
	outReq.Host = outReq.URL.Host

	resp, err := p.httpClient.Do(outReq)
	if err != nil {
		http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copy response headers
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	// If streaming SSE
	isStream := parsedReq.Stream || strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")
	if isStream {
		p.handleStreamingResponse(w, resp.Body, modelName, provider, userIntent)
	} else {
		p.handleJSONResponse(w, resp.Body, modelName, provider, userIntent)
	}
}

func (p *GatewayProxy) handleJSONResponse(w http.ResponseWriter, body io.Reader, modelName, provider, intent string) {
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

	p.recordRun(modelName, provider, promptTokens, completionTokens, intent)
}

func (p *GatewayProxy) handleStreamingResponse(w http.ResponseWriter, body io.Reader, modelName, provider, intent string) {
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

	// Parse SSE usage chunks if available
	fullSSE := capturedBuffer.String()
	if strings.Contains(fullSSE, "usage") {
		// Look for "usage":{"prompt_tokens":...}
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

	// If streaming didn't provide usage chunk, fallback to length estimate
	if promptTokens == 0 && completionTokens == 0 {
		completionTokens = int64(len(fullSSE) / 4)
		promptTokens = 1000
	}

	p.recordRun(modelName, provider, promptTokens, completionTokens, intent)
}

func (p *GatewayProxy) recordRun(modelName, provider string, promptTokens, completionTokens int64, intent string) {
	if p.cfg.Reporter == nil {
		return
	}

	cost := models.Global.CalculateCost(modelName, promptTokens, completionTokens)
	runID := randomID("proxy-run")

	_ = p.cfg.Reporter.ReportRun(ipc.TelemetryReport{
		RunID:            runID,
		TaskID:           "ProxyGateway",
		AgentName:        "AI-Proxy",
		ModelName:        modelName,
		Provider:         provider,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		CostUSD:          cost,
		Intent:           intent,
	})
}

func randomID(prefix string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%s", prefix, hex.EncodeToString(b))
}
