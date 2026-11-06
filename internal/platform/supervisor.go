// supervisor.go 定义 Platform 总管并持有内部组件，管理资源所有权。

package platform

import (
	"context"
	"path/filepath"

	"elbot/internal/config"
	"elbot/internal/platform/cli"
	"elbot/internal/platform/dispatch"
	"elbot/internal/platform/notification"
	"elbot/internal/platform/share"
	"elbot/internal/signal"
)

// Supervisor 是 App 对接的 Platform 总管；内部组件不通过 getter 对外暴露。
type Supervisor struct {
	lifecycle
	primary         share.Runtime
	adapters        []share.Runtime
	dispatcher      *dispatch.Router
	notifications   *notification.Manager
	connections     []*signal.Connection            // Platform 持有的通知接线，最终关闭时释放。
	ui              *cli.UI                         // CLI 展示同时提供思考流；资源关闭由接线闭包登记。
	registered      []registration                  // 根部固定的静态声明及构造顺序，构造完成后只读。
	agentWiring     []func(AgentCapabilities) error // 构造时登记的最小 Agent 接线，不通过实例类型猜测能力。
	agentCloseSteps []closeStep                     // Agent 接线取得的自有资源，先于通知及适配器关闭。
	outputWiring    []func() error                  // 连接可投递实例的普通发送及通知能力。
	agent           AgentCapabilities
	agentConnected  bool // ConnectAgent 只执行一次，失败结果也保留供 Start 检查。
	agentErr        error
}

// NewSupervisor 接收完整配置；部分构造失败仍返回总管，由 App 统一清理。
func NewSupervisor(ctx context.Context, cfg *config.Config, deps SupervisorDeps) (*Supervisor, error) {
	m := &Supervisor{}
	m.lifecycle.init(ctx)
	err := m.wire(cfg, deps, registrations(filepath.Join(filepath.Dir(cfg.ConfigPath), "frontend")))
	if err != nil {
		m.prepare = func() error { return err }
	}
	m.configureCloseSteps()
	return m, err
}
