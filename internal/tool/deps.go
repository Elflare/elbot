// 声明 Tool 借用的共享资源和外部业务能力。
package tool

import (
	"context"
	"elbot/internal/delivery"

	"elbot/internal/media"
	"elbot/internal/modelmgr"
	"elbot/internal/security"
	"elbot/internal/storage"
)

// SupervisorDeps 只声明借用能力；配置单独传给构造入口，资源生命周期仍由提供方管理。
type SupervisorDeps struct {
	Store       storage.Store        // 读取和提交工具状态、Cron 任务定义。
	Models      *modelmgr.Service    // 校验 Cron 任务选择的模型。
	Media       *media.Manager       // 借用媒体管理能力，交付策略由 Media 处理。
	Permission  *security.Permission // 统一提供工具访问和执行确认策略。
	ChatHistory interface {
		Search(context.Context, storage.ChatHistorySearchRequest) ([]storage.ChatMessage, error)
		Around(context.Context, storage.ChatHistoryAroundRequest) ([]storage.ChatMessage, error)
		GetByPlatformMessage(context.Context, string, string, string) (*storage.ChatMessage, error)
	} // 只借用历史查询能力，数据库由 App 持有。
	Platform interface {
		ResolveMedia(context.Context, string, media.PlatformMedia, int64) (delivery.Source, error)
	} // 只借用媒体来源解析能力，不取得适配器。
}
