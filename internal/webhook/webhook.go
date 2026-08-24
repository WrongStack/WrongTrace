package webhook

import (
	"bytes"
	"context"
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
}

// Dispatcher broadcasts security and thrashing alerts asynchronously.
type Dispatcher struct {
	cfg        Config
	httpClient *http.Client
	mu         sync.RWMutex
}

// NewDispatcher constructs a Dispatcher.
func NewDispatcher(cfg Config) *Dispatcher {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Dispatcher{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// UpdateConfig dynamically updates target webhook URLs.
func (d *Dispatcher) UpdateConfig(cfg Config) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cfg = cfg
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

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if genericURL != "" {
			_ = d.sendGeneric(ctx, genericURL, p)
		}
		if slackURL != "" {
			_ = d.sendSlack(ctx, slackURL, p)
		}
		if discordURL != "" {
			_ = d.sendDiscord(ctx, discordURL, p)
		}
	}()
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
	resp, err := d.httpClient.Do(req)
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
	resp, err := d.httpClient.Do(req)
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
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}
