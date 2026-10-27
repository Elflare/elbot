// adapter.go 定义适配器、最小借用依赖与生命周期状态。
package telegram

import (
	"context"
	"elbot/internal/command"
	"elbot/internal/platform/share"
	"sync"
)

// Deps 由 Platform 根部交付配置与实际需要的借用能力。
type Deps struct {
	share.CommonConfig
	ChatHistory       share.ChatHistoryWriter                                                      // 只借用历史写入能力，不拥有历史库。
	ReferenceResolver func(context.Context, share.ReferenceOptions) (share.ReferenceResult, error) // 引用恢复能力。
}

type Adapter struct {
	cfg               Config
	referenceResolver func(context.Context, share.ReferenceOptions) (share.ReferenceResult, error) // 根部注入的引用恢复能力。
	chatHistory       share.ChatHistoryWriter                                                      // 借用的历史写入能力。
	client            *apiClient                                                                   // 自有 HTTP 客户端；禁用时不创建。
	commandCatalog    []command.Info                                                               // 根部交付的命令目录，由 mu 保护。

	mu               sync.Mutex
	started          bool            // 已尝试启动，重复启动返回同一结果。
	closing          bool            // 已停止入站接纳与轮询。
	closingResources bool            // 已停止操作准入并开始最终清理。
	startErr         error           // 首次启动结果。
	workCtx          context.Context // 轮询和已接纳入站处理的生命周期。
	workCancel       context.CancelFunc
	operationCtx     context.Context // 发送、API、媒体及流式操作，只由最终关闭取消。
	operationCancel  context.CancelFunc
	stopParent       func() bool    // 解除运行上下文的取消观察。
	pollDone         chan struct{}  // 轮询实际退出。
	done             chan struct{}  // 轮询与入站处理全部退出。
	closeDone        chan struct{}  // 完整操作退出且自有资源清理完成。
	work             sync.WaitGroup // 已接纳的完整入站处理。
	operations       sync.WaitGroup // 已接纳的完整外发、媒体及流式调用。
}

func (a *Adapter) Name() string  { return platformName }
func (a *Adapter) Enabled() bool { return a.cfg.Enabled }
