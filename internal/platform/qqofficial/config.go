package qqofficial

import (
	"fmt"
	"os"
	"strings"
	"time"

	"elbot/internal/platform"
	"elbot/internal/storage"
)

const (
	platformName       = "qqofficial"
	defaultAPIBaseURL  = "https://api.sgroup.qq.com"
	defaultTokenURL    = "https://bots.qq.com/app/getAppAccessToken"
	defaultHTTPTimeout = 15 * time.Second
)

type Config struct {
	Enabled                  bool   `toml:"enabled"`
	AppID                    string `toml:"app_id"`
	ClientSecret             string `toml:"client_secret"`
	ClientSecretEnv          string `toml:"client_secret_env"`
	AllowProactive           *bool  `toml:"allow_proactive"`
	MarkdownByDefault        *bool  `toml:"markdown_by_default"`
	EnableKeyboard           *bool  `toml:"enable_keyboard"`
	EnableArk                bool   `toml:"enable_ark"`
	HTTPTimeoutSeconds       int    `toml:"http_timeout_seconds"`
	ReconnectIntervalSeconds int    `toml:"reconnect_interval_seconds"`
	AttachmentDir            string `toml:"-"`
	MaxReceiveFileBytes      int64  `toml:"-"`
	DownloadTimeoutSecs      int    `toml:"-"`
	CommandPrefixes          []string
	Superadmins              []string
}

func NewFromPlatformConfig(raw map[string]any, store storage.Store, logger Logger, superadmins []string, commandPrefixes []string, attachmentDir string, maxReceiveFileBytes int64, downloadTimeoutSecs int) (*Adapter, error) {
	var cfg Config
	if err := platform.DecodeConfig(raw, &cfg); err != nil {
		return nil, fmt.Errorf("decode qqofficial config: %w", err)
	}
	cfg.Superadmins = append([]string(nil), superadmins...)
	cfg.CommandPrefixes = append([]string(nil), commandPrefixes...)
	cfg.AttachmentDir = strings.TrimSpace(attachmentDir)
	cfg.MaxReceiveFileBytes = maxReceiveFileBytes
	cfg.DownloadTimeoutSecs = downloadTimeoutSecs
	applyDefaults(&cfg)
	if cfg.Enabled {
		if strings.TrimSpace(cfg.AppID) == "" {
			return nil, fmt.Errorf("qqofficial app_id is required")
		}
		if strings.TrimSpace(cfg.secret()) == "" {
			return nil, fmt.Errorf("qqofficial client_secret or client_secret_env is required")
		}
	}
	return New(cfg, store, logger), nil
}

func applyDefaults(cfg *Config) {
	if cfg.HTTPTimeoutSeconds <= 0 {
		cfg.HTTPTimeoutSeconds = int(defaultHTTPTimeout / time.Second)
	}
	if cfg.ReconnectIntervalSeconds <= 0 {
		cfg.ReconnectIntervalSeconds = 3
	}
	if cfg.AllowProactive == nil {
		value := true
		cfg.AllowProactive = &value
	}
	if cfg.MarkdownByDefault == nil {
		value := true
		cfg.MarkdownByDefault = &value
	}
	if cfg.EnableKeyboard == nil {
		value := true
		cfg.EnableKeyboard = &value
	}
	if cfg.MaxReceiveFileBytes <= 0 {
		cfg.MaxReceiveFileBytes = 100 * 1024 * 1024
	}
	if cfg.DownloadTimeoutSecs <= 0 {
		cfg.DownloadTimeoutSecs = 60
	}
}

func (c Config) secret() string {
	if value := strings.TrimSpace(c.ClientSecret); value != "" {
		return value
	}
	if env := strings.TrimSpace(c.ClientSecretEnv); env != "" {
		return strings.TrimSpace(os.Getenv(env))
	}
	return ""
}

func (c Config) httpTimeout() time.Duration {
	d := time.Duration(c.HTTPTimeoutSeconds) * time.Second
	if d <= 0 {
		return defaultHTTPTimeout
	}
	return d
}

func (c Config) allowProactive() bool {
	return c.AllowProactive == nil || *c.AllowProactive
}

func (c Config) markdownByDefault() bool {
	return c.MarkdownByDefault == nil || *c.MarkdownByDefault
}

func (c Config) enableKeyboard() bool {
	return c.EnableKeyboard == nil || *c.EnableKeyboard
}

func (c Config) reconnectInterval() time.Duration {
	d := time.Duration(c.ReconnectIntervalSeconds) * time.Second
	if d <= 0 {
		return 3 * time.Second
	}
	return d
}
