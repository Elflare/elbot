// deps.go 声明借用的共享资源与外部最小能力接口。

package platform

import (
	"context"
	"elbot/internal/contextinfo"

	"elbot/internal/agent"
	"elbot/internal/command"
	"elbot/internal/completion"
	"elbot/internal/control"
	"elbot/internal/media"
	"elbot/internal/operationresult"
	"elbot/internal/platform/cli"
	"elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

// SupervisorDeps 只借用共享资源，不取得它们的关闭所有权。
type SupervisorDeps struct {
	Mode    string                  // App 已验证的 tui 或 service 运行模式。
	Store   storage.Store           // 借用的公共业务存储；聊天历史由 ChatHistory 单独交付。
	Control cli.ControlCapabilities // CLI Web 管理 API 使用的统一业务操作。
	// ChatHistory 借用历史写入、别名和引用恢复能力，不取得关闭所有权。
	ChatHistory interface {
		Append(context.Context, *storage.ChatMessage) error
		MapPlatformMessageAlias(context.Context, storage.PlatformMessageAlias) error
		ResolvePlatformMessageAlias(context.Context, string, contextinfo.ChatType, string, string) (string, error)
		GetByPlatformMessage(context.Context, string, contextinfo.ChatType, string, string) (*storage.ChatMessage, error)
	}
	Media *media.MediaCenter // 由 dispatch 和远程 CLI 各自的消费接口裁剪媒体能力。
}

// AgentCapabilities 按目标 Agent 公开契约接线，不向子包交付完整总管。
// MessageRequest 由 Agent 根部定义，平台内部状态在根部转换。
type AgentCapabilities interface {
	ContinueControlInput(context.Context, control.Continuation) (context.Context, operationresult.Result[struct{}], error)
	ResolveReference(context.Context, session.Scope, storage.Message) (string, string, bool, error)
	HandleMessage(context.Context, agent.MessageRequest) error
	Complete(context.Context, completion.Request) ([]completion.Item, error)
	Commands() []command.Info
	ObserveStatusChanged(func(agent.StatusChangedEvent), signal.ConnectOptions) (*signal.Connection, error)
	ObserveBindingChanged(func(agent.BindingChangedEvent), signal.ConnectOptions) (*signal.Connection, error)
}
