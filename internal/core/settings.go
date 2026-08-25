package core

import (
	"encoding/json"
	"os"
	"path/filepath"
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
		Version:            "0.3.7",
	}
)

func init() {
	loadSettingsFromDisk()
}

func settingsFilePath() string {
	return filepath.Join(UserWrongTraceDir(), "settings.json")
}

func loadSettingsFromDisk() {
	path := settingsFilePath()
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var loaded AppSettings
	if err := json.Unmarshal(data, &loaded); err == nil {
		settingsMu.Lock()
		if loaded.DebounceMs > 0 {
			globalSettings.DebounceMs = loaded.DebounceMs
		}
		if len(loaded.IgnorePatterns) > 0 {
			globalSettings.IgnorePatterns = loaded.IgnorePatterns
		}
		if loaded.ThrashingThreshold > 0 {
			globalSettings.ThrashingThreshold = loaded.ThrashingThreshold
		}
		if loaded.FragilityCutoff > 0 {
			globalSettings.FragilityCutoff = loaded.FragilityCutoff
		}
		if loaded.CostAlertUSD > 0 {
			globalSettings.CostAlertUSD = loaded.CostAlertUSD
		}
		if loaded.AutoPruneDays > 0 {
			globalSettings.AutoPruneDays = loaded.AutoPruneDays
		}
		if loaded.DefaultProvider != "" {
			globalSettings.DefaultProvider = loaded.DefaultProvider
		}
		globalSettings.SlackWebhookURL = loaded.SlackWebhookURL
		globalSettings.DiscordWebhookURL = loaded.DiscordWebhookURL
		globalSettings.CustomWebhookURL = loaded.CustomWebhookURL
		settingsMu.Unlock()
	}
}

func saveSettingsToDisk(s AppSettings) {
	path := settingsFilePath()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if data, err := json.MarshalIndent(s, "", "  "); err == nil {
		if os.WriteFile(path, data, 0o600) == nil {
			_ = os.Chmod(path, 0o600)
		}
	}
}

// GetSettings returns a snapshot of the current settings.
func (e *Engine) GetSettings() AppSettings {
	settingsMu.RLock()
	s := globalSettings
	settingsMu.RUnlock()

	if e != nil && s.DBPath == "" {
		if active := e.GetActiveProject(); active != nil && active.DBPath != "" {
			s.DBPath = active.DBPath
		} else {
			s.DBPath = filepath.Join(UserWrongTraceDir(), "wrongtrace.db")
		}
	}
	return s
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

	saveSettingsToDisk(globalSettings)

	if e != nil && e.webhooks != nil {
		e.webhooks.UpdateConfig(webhook.Config{
			SlackURL:   globalSettings.SlackWebhookURL,
			DiscordURL: globalSettings.DiscordWebhookURL,
			GenericURL: globalSettings.CustomWebhookURL,
		})
	}

	return globalSettings
}
