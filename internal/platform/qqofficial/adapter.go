// adapter.go 定义 QQ Official 适配器、最小借用依赖及持有状态。
package qqofficial

import (
	"context"
	"elbot/internal/command"
	"elbot/internal/platform/share"
	"elbot/internal/storage"
	"sync"
)

// Deps 由 Platform 根部交付配置与实际需要的借用能力。
type Deps struct {
	share.CommonConfig
	ParseCommand func(string) command.Parsed // 根部绑定配置前缀的命令解析能力。
	// ChatHistory 只写历史及维护平台消息别名，不拥有历史库。
	ChatHistory interface {
		share.ChatHistoryWriter
		MapPlatformMessageAlias(context.Context, storage.PlatformMessageAlias) error
		ResolvePlatformMessageAlias(context.Context, string, string, string) (string, error)
	}
	ReferenceResolver func(context.Context, share.ReferenceOptions) (share.ReferenceResult, error) // 引用恢复能力。
}

type Adapter struct {
	cfg               Config
	parseCommand      func(string) command.Parsed                                                  // 借用的命令解析函数。
	referenceResolver func(context.Context, share.ReferenceOptions) (share.ReferenceResult, error) // 借用的引用恢复函数。
	chatHistory       interface {
		share.ChatHistoryWriter
		MapPlatformMessageAlias(context.Context, storage.PlatformMessageAlias) error
		ResolvePlatformMessageAlias(context.Context, string, string, string) (string, error)
	}
	client    *apiClient // 自有 HTTP 客户端与连接池，保留到最终关闭。
	seqMu     sync.Mutex
	seqByID   map[string]int // 各回复消息的发送序号。
	wsWriteMu sync.Mutex

	mu               sync.Mutex
	started          bool            // 已尝试启动，重复启动返回同一结果。
	closing          bool            // 已停止接纳入站及重连。
	closingResources bool            // 已停止发送准入并开始最终清理。
	startErr         error           // 首次启动结果。
	runErr           error           // 已记录的独立运行故障。
	workCtx          context.Context // 接入及已接纳业务的生命周期。
	workCancel       context.CancelFunc
	sendCtx          context.Context // 只由最终关闭取消，不随接入停止取消。
	sendCancel       context.CancelFunc
	stopParent       func() bool    // 解除运行上下文的取消观察。
	gatewayDone      chan struct{}  // 网关驱动及心跳实际退出。
	done             chan struct{}  // 接入及业务回调实际退出。
	closeDone        chan struct{}  // 发送与自有资源实际清理完成。
	work             sync.WaitGroup // 已接纳的完整入站处理。
	sends            sync.WaitGroup // 完整发送调用，包含回执别名入库。
}

func (a *Adapter) Name() string  { return platformName }
func (a *Adapter) Enabled() bool { return a.cfg.Enabled }
