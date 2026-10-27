// config.go 解析和规范化配置、解析凭据，并提供 NewFromPlatformConfig 构造入口。

package qqofficial

import (
	"context"
	"elbot/internal/platform/share"
	"fmt"
	"strings"
	"time"

	"elbot/internal/config"
)

const (
	platformName       = "qqofficial"
	defaultAPIBaseURL  = "https://api.sgroup.qq.com"
	defaultTokenURL    = "https://bots.qq.com/app/getAppAccessToken"
	defaultHTTPTimeout = 15 * time.Second
)

type Config struct {
	Enabled                  bool     `toml:"enabled"`
	AppID                    string   `toml:"app_id"`
	ClientSecret             string   `toml:"client_secret"`
	ClientSecretEnv          string   `toml:"client_secret_env"`
	AllowProactive           *bool    `toml:"allow_proactive"`
	MarkdownByDefault        *bool    `toml:"markdown_by_default"`
	EnableKeyboard           *bool    `toml:"enable_keyboard"`
	HTTPTimeoutSeconds       int      `toml:"http_timeout_seconds"`
	ReconnectIntervalSeconds int      `toml:"reconnect_interval_seconds"`
	TriggerKeywords          []string `toml:"trigger_keywords"`
	CommandPrefixes          []string
	Superadmins              []string
}

func NewFromPlatformConfig(raw map[string]any, deps Deps) (*Adapter, error) {
	var cfg Config
	if err := share.DecodeConfig(raw, &cfg); err != nil {
		return nil, fmt.Errorf("decode qqofficial config: %w", err)
	}
	cfg.Superadmins = append([]string(nil), deps.Superadmins...)
	cfg.CommandPrefixes = append([]string(nil), deps.CommandPrefixes...)
	applyDefaults(&cfg)
	if cfg.Enabled {
		if strings.TrimSpace(cfg.AppID) == "" {
			return nil, fmt.Errorf("qqofficial app_id is required")
		}
		secret, err := resolveSecret(cfg, deps.ConfigDir)
		if err != nil {
			return nil, err
		}
		if secret == "" {
			return nil, fmt.Errorf("qqofficial client_secret or client_secret_env is required")
		}
		cfg.ClientSecret = secret
	}
	sendCtx, sendCancel := context.WithCancel(context.Background())
	adapter := &Adapter{
		cfg: cfg, chatHistory: deps.ChatHistory, referenceResolver: deps.ReferenceResolver,
		client: newAPIClient(cfg), seqByID: make(map[string]int),
		sendCtx: sendCtx, sendCancel: sendCancel,
		gatewayDone: make(chan struct{}), done: make(chan struct{}), closeDone: make(chan struct{}),
	}
	return adapter, nil
}

func resolveSecret(cfg Config, configEnvDir string) (string, error) {
	if value := strings.TrimSpace(cfg.ClientSecret); value != "" {
		return value, nil
	}
	envName := strings.TrimSpace(cfg.ClientSecretEnv)
	if envName == "" {
		return "", nil
	}
	value, _, err := config.ConfigEnv(envName, configEnvDir)
	if err != nil {
		return "", fmt.Errorf("resolve qqofficial client secret from %s: %w", envName, err)
	}
	return strings.TrimSpace(value), nil
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
}

func (c Config) httpTimeout() time.Duration {
	return time.Duration(c.HTTPTimeoutSeconds) * time.Second
}

func (c Config) allowProactive() bool {
	return *c.AllowProactive
}

func (c Config) markdownByDefault() bool {
	return *c.MarkdownByDefault
}

func (c Config) enableKeyboard() bool {
	return *c.EnableKeyboard
}

func (c Config) reconnectInterval() time.Duration {
	return time.Duration(c.ReconnectIntervalSeconds) * time.Second
}
