package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// EventType categorizes the notification alert.
type EventType string

const (
	EventThrashingAlert EventType = "thrashing_alert"
	EventSpendAlert     EventType = "spend_limit_exceeded"
	EventSelfRollback   EventType = "self_rollback_detected"
	EventGuardrailBlock EventType = "guardrail_blocked"
)

// Payload represents the structured notification sent to external webhooks.
type Payload struct {
	EventType EventType              `json:"event_type"`
	Severity  string                 `json:"severity"` // info, warning, critical
	Message   string                 `json:"message"`
	Details   map[string]interface{} `json:"details,omitempty"`
	Timestamp string                 `json:"timestamp"`
}

// Config configures the webhook dispatcher.
type Config struct {
	SlackURL   string
	DiscordURL string
	GenericURL string
	Timeout    time.Duration
	// SigningSecret, when set, stamps every generic-endpoint delivery with
	// X-WrongTrace-Signature: sha256=<hex HMAC-SHA256 of the exact request
	// body>. Receivers verify with the shared secret; Slack and Discord have
	// their own URL-embedded credentials and are left untouched.
	SigningSecret string
}

// Dispatcher broadcasts security and thrashing alerts asynchronously.
type Dispatcher struct {
	cfg        Config
	httpClient *http.Client
	mu         sync.RWMutex
	inFlight   chan struct{}
}

const maxConcurrentDeliveries = 16

// defaultDispatchTimeout bounds one delivery attempt when Config.Timeout is
// unset or non-positive.
const defaultDispatchTimeout = 5 * time.Second

// resolveTimeout normalizes the per-request deadline. It is the single source
// of truth for construction and for runtime updates alike, so an omitted
// Timeout can never reach the client as 0 (which means unlimited).
func resolveTimeout(cfg Config) time.Duration {
	if cfg.Timeout <= 0 {
		return defaultDispatchTimeout
	}
	return cfg.Timeout
}

// NewDispatcher constructs a Dispatcher.
func NewDispatcher(cfg Config) *Dispatcher {
	return &Dispatcher{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: resolveTimeout(cfg)},
		inFlight:   make(chan struct{}, maxConcurrentDeliveries),
	}
}

// InFlight returns the number of concurrent deliveries currently in-flight (0–maxConcurrentDeliveries).
// This is the live semaphore occupancy that drives the watcher's fsnotify event log.
func (d *Dispatcher) InFlight() int { return len(d.inFlight) }

// UpdateConfig dynamically updates target webhook URLs and the per-request
// timeout. An http.Client fixes its deadline at construction, so assigning
// d.cfg alone left every runtime Timeout change inert. The client is
// republished here instead; it owns no transport (http.DefaultTransport is
// shared), so swapping it is cheap and retains no per-client state.
func (d *Dispatcher) UpdateConfig(cfg Config) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cfg = cfg
	d.httpClient = &http.Client{Timeout: resolveTimeout(cfg)}
}

// client snapshots the active HTTP client under the read lock. The senders call
// Do outside the lock, so a delivery already in flight keeps the deadline it
// started with while a concurrent update swaps in the next client.
func (d *Dispatcher) client() *http.Client {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.httpClient
}

// Dispatch sends notifications to all configured webhook endpoints in the background.
func (d *Dispatcher) Dispatch(p Payload) {
	if p.Timestamp == "" {
		p.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}

	d.mu.RLock()
	slackURL := d.cfg.SlackURL
	discordURL := d.cfg.DiscordURL
	genericURL := d.cfg.GenericURL
	d.mu.RUnlock()

	if slackURL == "" && discordURL == "" && genericURL == "" {
		return
	}
	select {
	case d.inFlight <- struct{}{}:
	default:
		// Alerts are best-effort. A broken/slow endpoint must not turn a burst of
		// guardrail checks into an unbounded goroutine and request-body backlog.
		return
	}

	go func() {
		defer func() { <-d.inFlight }()
		// Each endpoint is delivered under its OWN deadline derived from
		// Config.Timeout. A single batch-wide context was wrong twice over: it
		// silently truncated any Timeout above it, and because the sends run
		// sequentially the first slow endpoint ate the budget the rest needed.
		timeout := d.deliveryTimeout()
		d.deliver(d.sendGeneric, genericURL, p, timeout)
		d.deliver(d.sendSlack, slackURL, p, timeout)
		d.deliver(d.sendDiscord, discordURL, p, timeout)
	}()
}

// deliveryTimeout returns the per-request deadline to enforce. It mirrors the
// active http.Client so the surrounding context can never be tighter than the
// client's own timeout, which would silently override the caller's config.
func (d *Dispatcher) deliveryTimeout() time.Duration {
	if c := d.client(); c != nil && c.Timeout > 0 {
		return c.Timeout
	}
	return defaultDispatchTimeout
}

// deliver performs one endpoint's send under its own deadline. Empty targets are
// skipped, and delivery stays best-effort: a failing endpoint must never abort
// the remaining ones.
func (d *Dispatcher) deliver(send func(context.Context, string, Payload) error, url string, p Payload, timeout time.Duration) {
	if url == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_ = send(ctx, url, p)
}

func (d *Dispatcher) sendGeneric(ctx context.Context, url string, p Payload) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	d.mu.RLock()
	secret := d.cfg.SigningSecret
	d.mu.RUnlock()
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(b)
		req.Header.Set("X-WrongTrace-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := d.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func (d *Dispatcher) sendSlack(ctx context.Context, url string, p Payload) error {
	color := "#6366f1"
	if p.Severity == "critical" {
		color = "#f43f5e"
	} else if p.Severity == "warning" {
		color = "#f59e0b"
	}

	slackBody := map[string]interface{}{
		"attachments": []map[string]interface{}{
			{
				"color": color,
				"title": fmt.Sprintf("[WrongTrace] %s", p.EventType),
				"text":  p.Message,
				"ts":    time.Now().Unix(),
			},
		},
	}
	b, _ := json.Marshal(slackBody)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func (d *Dispatcher) sendDiscord(ctx context.Context, url string, p Payload) error {
	color := 0x6366f1
	if p.Severity == "critical" {
		color = 0xf43f5e
	} else if p.Severity == "warning" {
		color = 0xf59e0b
	}

	discordBody := map[string]interface{}{
		"embeds": []map[string]interface{}{
			{
				"title":       fmt.Sprintf("WrongTrace Alert: %s", p.EventType),
				"description": p.Message,
				"color":       color,
				"timestamp":   p.Timestamp,
			},
		},
	}
	b, _ := json.Marshal(discordBody)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}
