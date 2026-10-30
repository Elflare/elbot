// adapter.go 定义 OneBot 配置、借用依赖与适配器，通过唯一入口构造实例。
package qqonebot

import (
	"context"
	"elbot/internal/command"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/platform/share"
	"elbot/internal/storage"
	"fmt"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Enabled                  bool     `toml:"enabled"`
	URL                      string   `toml:"ws_url"`
	AccessToken              string   `toml:"access_token"`
	AccessTokenEnv           string   `toml:"access_token_env"`
	ReconnectIntervalSeconds int      `toml:"reconnect_interval_seconds"`
	APITimeoutSeconds        int      `toml:"api_timeout_seconds"`
	TriggerKeywords          []string `toml:"trigger_keywords"`
	SendFileMode             string   `toml:"send_file_mode"`
	Superadmins              []string `toml:"-"`
}

// Deps 是 OneBot 构造所需的配置与借用能力，由 Platform 根部装配。
type Deps struct {
	share.CommonConfig
	ParseCommand func(string) command.Parsed // 根部绑定配置前缀的命令解析能力。
	// ChatHistory 只写入历史和查询被引用消息的身份。
	ChatHistory interface {
		share.ChatHistoryWriter
		share.ChatHistoryReader
	}
	ReferenceResolver     func(context.Context, share.ReferenceOptions) (share.ReferenceResult, error)            // 引用恢复能力。
	FindByPlatformMessage func(context.Context, string, string, string) (*storage.Message, error)                 // 查询消息输出关联。
	FindOutputs           func(context.Context, string, string, string, time.Time) ([]storage.MediaOutput, error) // 查询未过期的媒体输出关联。
}

type Adapter struct {
	cfg               Config
	parseCommand      func(string) command.Parsed                                                  // 借用的命令解析函数。
	referenceResolver func(context.Context, share.ReferenceOptions) (share.ReferenceResult, error) // 根部注入的引用恢复能力。
	chatHistory       interface {
		share.ChatHistoryWriter
		share.ChatHistoryReader
	}
	findByPlatformMessage func(context.Context, string, string, string) (*storage.Message, error)                 // 借用的消息关联查询。
	findOutputs           func(context.Context, string, string, string, time.Time) ([]storage.MediaOutput, error) // 借用的媒体输出关联查询。
	transport             *Transport                                                                              // 常驻连接，保留到最终关闭。
	lastMessageID         int64                                                                                   // 上一条非零入站 ID，断线重连时保留。

	mu               sync.Mutex
	started          bool            // 已尝试启动，重复 Start 不替换上下文或 handler。
	closing          bool            // 入站及新临时拨号已停止。
	closingResources bool            // 最终清理已启动，重试只等待同一次清理。
	workCtx          context.Context // 常驻拨号、重连及已接纳业务的上下文。
	workCancel       context.CancelFunc
	transportCtx     context.Context // 隔离运行取消的协议读取上下文。
	transportCancel  context.CancelFunc
	stopParent       func() bool                       // 解除运行上下文的取消观察。
	connectDone      chan struct{}                     // 常驻拨号与重连任务已退出。
	done             chan struct{}                     // 接入及已接纳业务实际退出。
	closeDone        chan struct{}                     // 全部连接和自有协程实际退出。
	work             sync.WaitGroup                    // 已接纳的入站处理。
	transports       sync.WaitGroup                    // 常驻读取协程及临时发送调用。
	temporary        map[*Transport]context.CancelFunc // 临时连接所有权及各自的拨号取消函数。
	closeErr         error                             // 最终清理结果。
}

func NewFromPlatformConfig(raw map[string]any, deps Deps) (*Adapter, error) {
	var cfg Config
	if err := share.DecodeConfig(raw, &cfg); err != nil {
		return nil, fmt.Errorf("decode qqonebot config: %w", err)
	}
	cfg.Superadmins = append([]string(nil), deps.Superadmins...)
	applyDefaults(&cfg)
	if err := validateSendFileMode(cfg.SendFileMode); err != nil {
		return nil, err
	}
	envName := strings.TrimSpace(cfg.AccessTokenEnv)
	if strings.TrimSpace(cfg.AccessToken) == "" && envName != "" {
		value, _, err := config.ConfigEnv(envName, deps.ConfigDir)
		if err != nil {
			return nil, fmt.Errorf("resolve qqonebot access token from %s: %w", envName, err)
		}
		cfg.AccessToken = strings.TrimSpace(value)
	}
	return &Adapter{
		parseCommand: deps.ParseCommand,
		cfg:          cfg, chatHistory: deps.ChatHistory, referenceResolver: deps.ReferenceResolver,
		findByPlatformMessage: deps.FindByPlatformMessage, findOutputs: deps.FindOutputs,
		transport:   newTransport(cfg.URL, cfg.AccessToken, time.Duration(cfg.APITimeoutSeconds)*time.Second),
		connectDone: make(chan struct{}), done: make(chan struct{}), closeDone: make(chan struct{}),
		temporary: make(map[*Transport]context.CancelFunc),
	}, nil
}

func applyDefaults(cfg *Config) {
	if cfg.URL == "" {
		cfg.URL = "ws://127.0.0.1:6700/"
	}
	if cfg.ReconnectIntervalSeconds <= 0 {
		cfg.ReconnectIntervalSeconds = 3
	}
	if cfg.APITimeoutSeconds <= 0 {
		cfg.APITimeoutSeconds = 15
	}
	cfg.SendFileMode = strings.ToLower(strings.TrimSpace(cfg.SendFileMode))
	if cfg.SendFileMode == "" {
		cfg.SendFileMode = sendFileModeBase64
	}
}

func validateSendFileMode(mode string) error {
	switch mode {
	case sendFileModeBase64, sendFileModeFileURI:
		return nil
	default:
		return fmt.Errorf("qqonebot send_file_mode must be %q or %q, got %q", sendFileModeBase64, sendFileModeFileURI, mode)
	}
}

func (a *Adapter) Name() string { return "qqonebot" }

func (a *Adapter) Enabled() bool { return a.cfg.Enabled }

func logIdentity(ctx context.Context) events.LogIdentity {
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	var identity events.LogIdentity
	if facts.SessionID != "" {
		identity.SessionID = &facts.SessionID
	}
	if facts.RunID != "" {
		identity.RunID = &facts.RunID
	}
	if facts.Attempt != "" {
		identity.Attempt = &facts.Attempt
	}
	if facts.RequestID != "" {
		identity.RequestID = &facts.RequestID
	}
	if facts.RootRequestID != "" {
		identity.RootRequestID = &facts.RootRequestID
	}
	return identity
}
