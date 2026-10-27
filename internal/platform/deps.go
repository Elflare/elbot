// deps.go 声明借用的共享资源与外部最小能力接口。

package platform

import (
	"context"

	"elbot/internal/agent"
	"elbot/internal/command"
	"elbot/internal/completion"
	"elbot/internal/logging"
	"elbot/internal/media"
	"elbot/internal/security"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

// SupervisorDeps 只借用共享资源，不取得它们的关闭所有权。
type SupervisorDeps struct {
	Mode  string        // App 已验证的 tui 或 service 运行模式。
	Store storage.Store // 借用的公共业务存储；聊天历史由 ChatHistory 单独交付。
	// ChatHistory 借用历史写入、别名和引用恢复能力，不取得关闭所有权。
	ChatHistory interface {
		Append(context.Context, *storage.ChatMessage) error
		MapPlatformMessageAlias(context.Context, storage.PlatformMessageAlias) error
		ResolvePlatformMessageAlias(context.Context, string, string, string) (string, error)
		GetByPlatformMessage(context.Context, string, string, string) (*storage.ChatMessage, error)
	}
	Media      *media.Manager       // 发送媒体准备所需能力，由 dispatch 的消费接口裁剪。
	Permission *security.Permission // 共享权限服务；不由 Platform 关闭。
	Logs       *logging.Manager     // 共享日志设施；不由 Platform 关闭。
}

// AgentCapabilities 按目标 Agent 公开契约接线，不向子包交付完整总管。
// MessageRequest 由 Agent 根部定义，平台内部状态在根部转换。
type AgentCapabilities interface {
	HandleMessage(context.Context, agent.MessageRequest) error
	Complete(context.Context, completion.Request) ([]completion.Item, error)
	Commands() []command.Info
	ObserveStatusChanged(func(agent.StatusChangedEvent) error, signal.ConnectOptions) (*signal.Connection, error)
	ObserveBindingChanged(func(agent.BindingChangedEvent) error, signal.ConnectOptions) (*signal.Connection, error)
}
