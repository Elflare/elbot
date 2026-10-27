// supervisor.go 定义 Platform 总管并持有内部组件，管理资源所有权。

package platform

import (
	"context"

	"elbot/internal/config"
	"elbot/internal/platform/cli"
	"elbot/internal/platform/dispatch"
	"elbot/internal/platform/notification"
	"elbot/internal/platform/share"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

// Supervisor 是 App 对接的 Platform 总管；内部组件不通过 getter 对外暴露。
type Supervisor struct {
	lifecycle
	primary        share.Runtime
	adapters       []share.Runtime
	dispatcher     *dispatch.Router
	notifications  *notification.Manager
	connections    []*signal.Connection // Platform 持有的通知接线，最终关闭时释放。
	ui             *cli.UI
	agent          AgentCapabilities
	agentConnected bool // ConnectAgent 只执行一次，失败结果也保留供 Start 检查。
	agentErr       error
	historyStore   *sqlite.ChatHistoryStore
	history        storage.ChatHistoryRepository
}

// NewSupervisor 接收完整配置；部分构造失败仍返回总管，由 App 统一清理。
func NewSupervisor(ctx context.Context, cfg *config.Config, deps SupervisorDeps) (*Supervisor, error) {
	m := &Supervisor{}
	m.lifecycle.init(ctx, deps.Mode)
	err := m.wire(cfg, deps)
	if err != nil {
		m.prepare = func() error { return err }
	}
	m.configureCloseSteps()
	return m, err
}
