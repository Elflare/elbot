// 声明 Tool 借用的共享资源和外部业务能力。
package tool

import (
	"context"

	"elbot/internal/media"
	"elbot/internal/modelmgr"
	"elbot/internal/security"
	"elbot/internal/storage"
)

// SupervisorDeps 只声明借用能力；配置单独传给构造入口，资源生命周期仍由提供方管理。
type SupervisorDeps struct {
	Store      storage.Store        // 读取和提交工具状态、Cron 任务定义。
	Models     *modelmgr.Service    // 校验 Cron 任务选择的模型。
	Media      *media.Manager       // 借用媒体管理能力，交付策略由 Media 处理。
	Permission *security.Permission // 统一提供工具访问和执行确认策略。
	Platform   interface {
		SearchChatHistory(context.Context, storage.ChatHistorySearchRequest) ([]storage.ChatMessage, error)
		ChatHistoryAround(context.Context, storage.ChatHistoryAroundRequest) ([]storage.ChatMessage, error)
		GetChatHistoryMessage(ctx context.Context, platform, scopeID, messageID string) (*storage.ChatMessage, error)
	} // 聊天历史及其存储由 Platform 持有，Tool 只查询消息。
}
