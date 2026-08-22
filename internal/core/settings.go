package core

import (
	"sync"

	"github.com/wrongstack/wrongtrace/internal/webhook"
)

// AppSettings holds dynamic daemon configuration and user preferences.
type AppSettings struct {
	DebounceMs         int      `json:"debounce_ms"`
	IgnorePatterns     []string `json:"ignore_patterns"`
	ThrashingThreshold int      `json:"thrashing_threshold"`
	FragilityCutoff    int      `json:"fragility_cutoff"`
	CostAlertUSD       float64  `json:"cost_alert_usd"`
	AutoPruneDays      int      `json:"auto_prune_days"`
	DefaultProvider    string   `json:"default_provider"`
	SlackWebhookURL    string   `json:"slack_webhook_url"`
	DiscordWebhookURL  string   `json:"discord_webhook_url"`
	CustomWebhookURL   string   `json:"custom_webhook_url"`
	DBPath             string   `json:"db_path"`
	SocketPath         string   `json:"socket_path"`
	Version            string   `json:"version"`
}

var (
	settingsMu     sync.RWMutex
	globalSettings = AppSettings{
		DebounceMs:         250,
		IgnorePatterns:     []string{".git", "node_modules", "vendor", "dist", "build", ".cache", "target", ".next"},
		ThrashingThreshold: 3,
		FragilityCutoff:    50,
		CostAlertUSD:       25.0,
		AutoPruneDays:      90,
		DefaultProvider:    "OpenAI",
		Version:            "0.2.0",
	}
)

// GetSettings returns a snapshot of the current settings.
func (e *Engine) GetSettings() AppSettings {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return globalSettings
}

// UpdateSettings updates the application settings.
func (e *Engine) UpdateSettings(s AppSettings) AppSettings {
	settingsMu.Lock()
	defer settingsMu.Unlock()

	if s.DebounceMs > 0 {
		globalSettings.DebounceMs = s.DebounceMs
	}
	if len(s.IgnorePatterns) > 0 {
		globalSettings.IgnorePatterns = s.IgnorePatterns
	}
	if s.ThrashingThreshold > 0 {
		globalSettings.ThrashingThreshold = s.ThrashingThreshold
	}
	if s.FragilityCutoff > 0 {
		globalSettings.FragilityCutoff = s.FragilityCutoff
	}
	if s.CostAlertUSD > 0 {
		globalSettings.CostAlertUSD = s.CostAlertUSD
	}
	if s.AutoPruneDays > 0 {
		globalSettings.AutoPruneDays = s.AutoPruneDays
	}
	if s.DefaultProvider != "" {
		globalSettings.DefaultProvider = s.DefaultProvider
	}
	if s.SlackWebhookURL == "-" || s.SlackWebhookURL == "none" || s.SlackWebhookURL == "CLEAR" {
		globalSettings.SlackWebhookURL = ""
	} else if s.SlackWebhookURL != "" {
		globalSettings.SlackWebhookURL = s.SlackWebhookURL
	}
	if s.DiscordWebhookURL == "-" || s.DiscordWebhookURL == "none" || s.DiscordWebhookURL == "CLEAR" {
		globalSettings.DiscordWebhookURL = ""
	} else if s.DiscordWebhookURL != "" {
		globalSettings.DiscordWebhookURL = s.DiscordWebhookURL
	}
	if s.CustomWebhookURL == "-" || s.CustomWebhookURL == "none" || s.CustomWebhookURL == "CLEAR" {
		globalSettings.CustomWebhookURL = ""
	} else if s.CustomWebhookURL != "" {
		globalSettings.CustomWebhookURL = s.CustomWebhookURL
	}

	if e != nil && e.webhooks != nil {
		e.webhooks.UpdateConfig(webhook.Config{
			SlackURL:   globalSettings.SlackWebhookURL,
			DiscordURL: globalSettings.DiscordWebhookURL,
			GenericURL: globalSettings.CustomWebhookURL,
		})
	}

	return globalSettings
}
