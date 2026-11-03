// config.go 解析配置并通过唯一入口构造适配器。

package telegram

import (
	"context"
	"elbot/internal/platform/share"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
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

type Config struct {
	Enabled                        bool     `toml:"enabled"`
	BotToken                       string   `toml:"bot_token"`
	BotTokenEnv                    string   `toml:"bot_token_env"`
	ProxyURL                       string   `toml:"proxy_url"`
	ProxyURLEnv                    string   `toml:"proxy_url_env"`
	APIBaseURL                     string   `toml:"api_base_url"`
	FileBaseURL                    string   `toml:"file_base_url"`
	APITimeoutSeconds              int      `toml:"api_timeout_seconds"`
	PollTimeoutSeconds             int      `toml:"poll_timeout_seconds"`
	ReconnectIntervalSeconds       int      `toml:"reconnect_interval_seconds"`
	StreamEditIntervalMilliseconds int      `toml:"stream_edit_interval_milliseconds"`
	Format                         string   `toml:"format"`
	TriggerKeywords                []string `toml:"trigger_keywords"`
}

func NewFromPlatformConfig(raw map[string]any, deps Deps) (*Adapter, error) {
	var cfg Config
	if err := share.DecodeConfig(raw, &cfg); err != nil {
		return nil, fmt.Errorf("decode telegram config: %w", err)
	}
	applyDefaults(&cfg)
	var client *apiClient
	if cfg.Enabled {
		token, err := cfg.tokenValue(deps.ConfigEnv)
		if err != nil {
			return nil, err
		}
		if token == "" {
			return nil, fmt.Errorf("telegram bot_token or bot_token_env is required")
		}
		proxyURL, err := cfg.proxyURL(deps.ConfigEnv)
		if err != nil {
			return nil, err
		}
		cfg.BotToken = token
		client = newAPIClient(cfg, proxyURL)
	}
	operationCtx, operationCancel := context.WithCancel(context.Background())
	adapter := &Adapter{
		name: deps.PlatformName, superadmins: deps.Superadmins,
		cfg: cfg, client: client, chatHistory: deps.ChatHistory, referenceResolver: deps.ReferenceResolver,
		operationCtx: operationCtx, operationCancel: operationCancel,
		pollDone: make(chan struct{}), done: make(chan struct{}), closeDone: make(chan struct{}),
	}
	return adapter, nil
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
}

func (c Config) tokenValue(configEnv func(string) (string, bool, error)) (string, error) {
	if value := strings.TrimSpace(c.BotToken); value != "" {
		return value, nil
	}
	return c.envValue(c.BotTokenEnv, configEnv)
}

func (c Config) proxyURL(configEnv func(string) (string, bool, error)) (*url.URL, error) {
	value, err := c.proxyValue(configEnv)
	if err != nil {
		return nil, err
	}
	if value == "" {
		return nil, nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		if err == nil {
			err = fmt.Errorf("missing scheme or host")
		}
		return nil, fmt.Errorf("invalid telegram proxy_url: %w", err)
	}
	return parsed, nil
}

func (c Config) proxyValue(configEnv func(string) (string, bool, error)) (string, error) {
	if value, err := c.envValue(c.ProxyURLEnv, configEnv); err != nil || value != "" {
		return value, err
	}
	return strings.TrimSpace(c.ProxyURL), nil
}

func (c Config) envValue(name string, configEnv func(string) (string, bool, error)) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil
	}
	value, ok, err := configEnv(name)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil
	}
	return strings.TrimSpace(value), nil
}

func (c Config) apiTimeout() time.Duration {
	return time.Duration(c.APITimeoutSeconds) * time.Second
}

func (c Config) pollTimeout() time.Duration {
	return time.Duration(c.PollTimeoutSeconds) * time.Second
}

func (c Config) reconnectInterval() time.Duration {
	return time.Duration(c.ReconnectIntervalSeconds) * time.Second
}

func (c Config) streamEditInterval() time.Duration {
	return time.Duration(c.StreamEditIntervalMilliseconds) * time.Millisecond
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

func (c Config) richMessageEnabled() bool {
	return c.Format == "rich"
}
