// 声明 Background 借用的共享资源和其他业务包的公开能力。
package background

import (
	"context"
	"encoding/json"
	"time"

	"elbot/internal/agent"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/logging"
	"elbot/internal/media"
	"elbot/internal/modelmgr"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

// SupervisorDeps 只借用资源和公开能力，生命周期仍由提供方管理；完整配置单独传入构造入口。
// 根部向子包注入实际需要的最小接口或函数，公开数据类型可以按需复用。
type SupervisorDeps struct {
	Store       storage.Store        // 持久化 Cron 任务及运行状态、Elnis 事件；不取得平台历史仓库。
	Models      *modelmgr.Service    // 解析和校验后台任务使用的模型选择。
	Media       *media.MediaCenter   // 提供媒体处理和维护能力。
	Logs        *logging.Manager     // 提供日志清理及保留策略。
	Agent       AgentCapabilities    // 执行后台任务、复制后台会话和清理过期会话。
	Tools       ToolCapabilities     // 展开后台工具声明并订阅 Cron 定义变更。
	Platform    PlatformCapabilities // 发送结果、调用平台 API。
	ChatHistory interface {
		DeleteBefore(context.Context, time.Time) (int, error)
	} // 仅借用历史清理能力，数据库由 App 持有。
}

// AgentCapabilities 只描述后台业务操作，不取得 Agent 的 Session、执行器或权限对象。
// Agent 根包提供请求、结果及以下公开方法；会话准备、执行上下文和工具预载由 Agent 负责。
type AgentCapabilities interface {
	RunBackground(context.Context, agent.BackgroundRequest) (agent.BackgroundResult, error)
	CopyBackground(context.Context, agent.BackgroundCopyRequest) (string, error)
	CleanupSessions(ctx context.Context, cutoff time.Time) (int, error)
	SessionTakenOver(context.Context, string) (bool, error)
}

// ToolCapabilities 消费 Tool 根包类型；订阅连接由 Background 持有和释放。
type ToolCapabilities interface {
	BackgroundSelections(context.Context, []string) ([]tool.BackgroundSelection, error)
	ObserveCronChanged(func(tool.CronChangedEvent) error, signal.ConnectOptions) (*signal.Connection, error)
}

// PlatformCapabilities 按平台公开能力接入。
// TODO: Platform 总管实现这些方法；Background 不兼容旧平台组件或调用器解析器。
type PlatformCapabilities interface {
	SendTo(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error)
	CallPlatformAPI(ctx context.Context, platform, api string, params map[string]any) (json.RawMessage, error)
}
