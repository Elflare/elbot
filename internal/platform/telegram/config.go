package telegram

import (
	"fmt"
	"os"
	"strings"
	"time"

	"elbot/internal/platform"
	"elbot/internal/storage"
)

const (
	platformName              = "telegram"
	defaultAPIBaseURL         = "https://api.telegram.org"
	defaultFileBaseURL        = "https://api.telegram.org/file"
	defaultAPITimeout         = 15 * time.Second
	defaultPollTimeout        = 30 * time.Second
	defaultReconnectInterval  = 3 * time.Second
	defaultStreamEditInterval = 250 * time.Millisecond
	defaultFormat             = "html"
	telegramTextPageRunes     = 4096
	telegramRichTextRunes     = 32768
)

type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
}

type Config struct {
	Enabled                        bool     `toml:"enabled"`
	BotToken                       string   `toml:"bot_token"`
	BotTokenEnv                    string   `toml:"bot_token_env"`
	APIBaseURL                     string   `toml:"api_base_url"`
	FileBaseURL                    string   `toml:"file_base_url"`
	APITimeoutSeconds              int      `toml:"api_timeout_seconds"`
	PollTimeoutSeconds             int      `toml:"poll_timeout_seconds"`
	ReconnectIntervalSeconds       int      `toml:"reconnect_interval_seconds"`
	StreamEditIntervalMilliseconds int      `toml:"stream_edit_interval_milliseconds"`
	Format                         string   `toml:"format"`
	TriggerKeywords                []string `toml:"trigger_keywords"`
	Superadmins                    []string `toml:"-"`
	CommandPrefixes                []string `toml:"-"`
}

func NewFromPlatformConfig(raw map[string]any, store storage.Store, chatHistory storage.ChatHistoryRepository, logger Logger, superadmins []string, commandPrefixes []string) (*Adapter, error) {
	var cfg Config
	if err := platform.DecodeConfig(raw, &cfg); err != nil {
		return nil, fmt.Errorf("decode telegram config: %w", err)
	}
	cfg.Superadmins = append([]string(nil), superadmins...)
	cfg.CommandPrefixes = append([]string(nil), commandPrefixes...)
	applyDefaults(&cfg)
	if cfg.Enabled && strings.TrimSpace(cfg.token()) == "" {
		return nil, fmt.Errorf("telegram bot_token or bot_token_env is required")
	}
	return New(cfg, store, chatHistory, logger), nil
}

func applyDefaults(cfg *Config) {
	if cfg.APIBaseURL == "" {
		cfg.APIBaseURL = defaultAPIBaseURL
	}
	if cfg.FileBaseURL == "" {
		cfg.FileBaseURL = defaultFileBaseURL
	}
	if cfg.APITimeoutSeconds <= 0 {
		cfg.APITimeoutSeconds = int(defaultAPITimeout / time.Second)
	}
	if cfg.PollTimeoutSeconds <= 0 {
		cfg.PollTimeoutSeconds = int(defaultPollTimeout / time.Second)
	}
	if cfg.ReconnectIntervalSeconds <= 0 {
		cfg.ReconnectIntervalSeconds = int(defaultReconnectInterval / time.Second)
	}
	if cfg.StreamEditIntervalMilliseconds <= 0 {
		cfg.StreamEditIntervalMilliseconds = int(defaultStreamEditInterval / time.Millisecond)
	}
	cfg.Format = normalizeTelegramFormat(cfg.Format)
	if len(cfg.CommandPrefixes) == 0 {
		cfg.CommandPrefixes = []string{"/"}
	}
}

func (c Config) token() string {
	if value := strings.TrimSpace(c.BotToken); value != "" {
		return value
	}
	if env := strings.TrimSpace(c.BotTokenEnv); env != "" {
		return strings.TrimSpace(os.Getenv(env))
	}
	return ""
}

func (c Config) apiTimeout() time.Duration {
	d := time.Duration(c.APITimeoutSeconds) * time.Second
	if d <= 0 {
		return defaultAPITimeout
	}
	return d
}

func (c Config) pollTimeout() time.Duration {
	d := time.Duration(c.PollTimeoutSeconds) * time.Second
	if d <= 0 {
		return defaultPollTimeout
	}
	return d
}

func (c Config) reconnectInterval() time.Duration {
	d := time.Duration(c.ReconnectIntervalSeconds) * time.Second
	if d <= 0 {
		return defaultReconnectInterval
	}
	return d
}

func (c Config) streamEditInterval() time.Duration {
	d := time.Duration(c.StreamEditIntervalMilliseconds) * time.Millisecond
	if d <= 0 {
		return defaultStreamEditInterval
	}
	return d
}

func normalizeTelegramFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "html":
		return defaultFormat
	case "rich", "plain":
		return strings.ToLower(strings.TrimSpace(format))
	default:
		return defaultFormat
	}
}

func (c Config) format() string {
	return normalizeTelegramFormat(c.Format)
}

func (c Config) richMessageEnabled() bool {
	return c.format() == "rich"
}
