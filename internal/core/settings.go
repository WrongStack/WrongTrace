package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
		Version:            "0.3.11",
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
		// Nil-check, not len>0, mirroring UpdateSettings: settings.json
		// omits the field to preserve the current list and writes [] to
		// clear it, so a persisted clear must survive the reload.
		if loaded.IgnorePatterns != nil {
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
		// saveSettingsToDisk persists the runtime paths the settings API
		// accepts, so they must survive the reload too — dropping them here
		// silently fell back to the active project / defaults on restart.
		if loaded.DBPath != "" {
			globalSettings.DBPath = loaded.DBPath
		}
		if loaded.SocketPath != "" {
			globalSettings.SocketPath = loaded.SocketPath
		}
		settingsMu.Unlock()
	}
}

func saveSettingsToDisk(s AppSettings) error {
	path := settingsFilePath()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure settings: %w", err)
	}
	return nil
}

// GetSettings returns a snapshot of the current settings.
func (e *Engine) GetSettings() AppSettings {
	settingsMu.RLock()
	s := globalSettings
	s.IgnorePatterns = slices.Clone(s.IgnorePatterns)
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
func (e *Engine) UpdateSettings(s AppSettings) (AppSettings, error) {
	settingsMu.Lock()
	defer settingsMu.Unlock()

	// Stage the patch so a failed save cannot publish unpersisted settings.
	next := globalSettings
	if s.DebounceMs > 0 {
		next.DebounceMs = s.DebounceMs
	}
	// Use nil-check, not len>0, so an explicitly empty slice clears the field.
	if s.IgnorePatterns != nil {
		next.IgnorePatterns = slices.Clone(s.IgnorePatterns)
	}
	if s.ThrashingThreshold > 0 {
		next.ThrashingThreshold = s.ThrashingThreshold
	}
	if s.FragilityCutoff > 0 {
		next.FragilityCutoff = s.FragilityCutoff
	}
	if s.CostAlertUSD > 0 {
		next.CostAlertUSD = s.CostAlertUSD
	}
	if s.AutoPruneDays > 0 {
		next.AutoPruneDays = s.AutoPruneDays
	}
	if s.DefaultProvider != "" {
		next.DefaultProvider = s.DefaultProvider
	}
	if s.SlackWebhookURL == "-" || s.SlackWebhookURL == "none" || s.SlackWebhookURL == "CLEAR" {
		next.SlackWebhookURL = ""
	} else if s.SlackWebhookURL != "" {
		next.SlackWebhookURL = s.SlackWebhookURL
	}
	if s.DiscordWebhookURL == "-" || s.DiscordWebhookURL == "none" || s.DiscordWebhookURL == "CLEAR" {
		next.DiscordWebhookURL = ""
	} else if s.DiscordWebhookURL != "" {
		next.DiscordWebhookURL = s.DiscordWebhookURL
	}
	if s.CustomWebhookURL == "-" || s.CustomWebhookURL == "none" || s.CustomWebhookURL == "CLEAR" {
		next.CustomWebhookURL = ""
	} else if s.CustomWebhookURL != "" {
		next.CustomWebhookURL = s.CustomWebhookURL
	}
	if s.DBPath != "" {
		next.DBPath = s.DBPath
	}
	if s.SocketPath != "" {
		next.SocketPath = s.SocketPath
	}

	if err := saveSettingsToDisk(next); err != nil {
		return AppSettings{}, err
	}
	globalSettings = next

	if e != nil && e.webhooks != nil {
		e.webhooks.UpdateConfig(webhook.Config{
			SlackURL:   next.SlackWebhookURL,
			DiscordURL: next.DiscordWebhookURL,
			GenericURL: next.CustomWebhookURL,
			// Env wins over any stored value so the HMAC secret never has to
			// live in settings.json next to the webhook URLs.
			SigningSecret: os.Getenv("WRONGTRACE_WEBHOOK_SECRET"),
		})
	}

	next.IgnorePatterns = slices.Clone(next.IgnorePatterns)
	return next, nil
}
