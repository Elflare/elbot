// 声明 Tool 借用的共享资源和外部业务能力。
package tool

import (
	"context"
	"elbot/internal/contextinfo"

	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/media"
	"elbot/internal/operationresult"
	"elbot/internal/security"
	"elbot/internal/storage"
)

// SupervisorDeps 只声明借用能力；配置单独传给构造入口，资源生命周期仍由提供方管理。
type SupervisorDeps struct {
	CreateQuestions func([]storage.Question) *storage.QuestionGroup
	Store           Store                // 读取和提交工具状态、Cron 任务定义。
	Models          ModelCapabilities    // 校验 Cron 任务选择的模型。
	Media           *media.MediaCenter   // 借用媒体管理能力，交付策略由 Media 处理。
	Permission      *security.Permission // 统一提供工具访问和执行确认策略。
	Platform        interface {
		ResolveMedia(context.Context, string, media.PlatformMedia, int64) (operationresult.Result[delivery.Source], error)
	} // 只借用媒体来源解析能力，不取得适配器。
}

// ModelCapabilities 只声明 Cron 任务模型选择的校验能力。
type ModelCapabilities interface {
	ValidateSelection(config.ModelSelection) operationresult.Result[struct{}]
}

// Store 是本业务借用的持久化操作，不拥有连接或关闭权。
type Store interface {
	cronStore
	GetSession(ctx context.Context, id string) (*storage.Session, error)
	MutateSession(ctx context.Context, id string, update func(*storage.Session) error) (*storage.Session, error)
	CreateToolCallRecord(ctx context.Context, record *storage.ToolCallRecord) error
	SearchChatHistory(context.Context, storage.ChatHistorySearchRequest) ([]storage.ChatMessage, error)
	ChatHistoryAround(context.Context, storage.ChatHistoryAroundRequest) ([]storage.ChatMessage, error)
	GetBySeq(context.Context, string, contextinfo.ChatType, string, int64, bool) (*storage.ChatMessage, error)
}
