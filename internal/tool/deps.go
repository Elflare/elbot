// 声明 Tool 借用的共享资源和外部业务能力。
package tool

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/platform"

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
		SearchChatHistory(context.Context, platform.ChatHistorySearchRequest) ([]platform.ChatMessage, error)
		ChatHistoryAround(context.Context, platform.ChatHistoryAroundRequest) ([]platform.ChatMessage, error)
		GetChatHistoryMessage(ctx context.Context, platform, scopeID, messageID string) (*platform.ChatMessage, error)
		ResolveMedia(context.Context, string, media.PlatformMedia, int64) (delivery.Source, error)
	} // 历史存储和适配器归 Platform；Tool 只借用查询和媒体来源解析能力。
}
