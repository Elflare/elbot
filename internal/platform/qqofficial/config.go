// config.go 解析和规范化配置、解析凭据，并提供 NewFromPlatformConfig 构造入口。
// TODO：消费本包最小依赖，将旧 New 初始化并入此入口。

package qqofficial

import (
	"elbot/internal/platform/share"
	"fmt"
	"os"
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
	EnableArk                bool     `toml:"enable_ark"`
	HTTPTimeoutSeconds       int      `toml:"http_timeout_seconds"`
	ReconnectIntervalSeconds int      `toml:"reconnect_interval_seconds"`
	AttachmentDir            string   `toml:"-"`
	MaxReceiveFileBytes      int64    `toml:"-"`
	DownloadTimeoutSecs      int      `toml:"-"`
	TriggerKeywords          []string `toml:"trigger_keywords"`
	CommandPrefixes          []string
	Superadmins              []string
}

// TODO：参考 qqonebot.Deps 改用本包依赖组合，并将旧 New 初始化并入本入口。
func NewFromPlatformConfig(raw map[string]any, deps share.AdapterDeps) (*Adapter, error) {
	var cfg Config
	if err := share.DecodeConfig(raw, &cfg); err != nil {
		return nil, fmt.Errorf("decode qqofficial config: %w", err)
	}
	cfg.Superadmins = append([]string(nil), deps.Superadmins...)
	cfg.CommandPrefixes = append([]string(nil), deps.CommandPrefixes...)
	cfg.AttachmentDir = strings.TrimSpace(deps.AttachmentDir)
	cfg.MaxReceiveFileBytes = deps.MaxReceiveFileBytes
	cfg.DownloadTimeoutSecs = deps.DownloadTimeoutSecs
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
	adapter := New(cfg, deps.ChatHistory)
	adapter.referenceResolver = deps.ReferenceResolver
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
	return *c.AllowProactive
}

func (c Config) markdownByDefault() bool {
	return *c.MarkdownByDefault
}

func (c Config) enableKeyboard() bool {
	return *c.EnableKeyboard
}

func (c Config) reconnectInterval() time.Duration {
	d := time.Duration(c.ReconnectIntervalSeconds) * time.Second
	if d <= 0 {
		return 3 * time.Second
	}
	return d
}
