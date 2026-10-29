package app

import (
	"context"
	"fmt"

	"elbot/internal/config"
	"elbot/internal/logging"
	"elbot/internal/media"
	"elbot/internal/modelmgr"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/storage/sqlite"
)

// sharedResources 记录已取得的共享资源，失败时也交给入口统一清理。
// 历史库与会话库分别持有，业务线只借用各自需要的能力。
type sharedResources struct {
	config       *config.Config           // 已加载的完整配置。
	logs         *logging.Manager         // App 持有的日志管理。
	store        storage.Store            // 使用方退出后关闭的共享存储。
	historyStore *sqlite.ChatHistoryStore // 所有历史使用方退出后关闭的独立历史库。
	media        *media.MediaCenter       // 共享媒体服务，借用 store。
	permission   *security.Permission     // 共享权限管理。
	models       *modelmgr.ModelManager   // 拥有模型选择状态、厂商快照与重试信号。
}

// prepareResources 只消费已定位路径；失败时返回部分结果，不自行清理。
func prepareResources(ctx context.Context, resolvedConfigPath string) (*sharedResources, error) {
	resources := &sharedResources{}
	if err := ctx.Err(); err != nil {
		return resources, err
	}
	cfg, err := config.Load(resolvedConfigPath)
	if err != nil {
		return resources, fmt.Errorf("load config: %w", err)
	}
	resources.config = cfg
	resources.logs, err = logging.NewManager(cfg.Runtime.LogLevel, cfg.Storage.SessionsSQLitePath, cfg.Runtime.LogRetentionDays)
	if err != nil {
		return resources, fmt.Errorf("prepare logs: %w", err)
	}
	store, err := sqlite.New(ctx, cfg.Storage.SessionsSQLitePath)
	if err != nil {
		return resources, fmt.Errorf("prepare storage: %w", err)
	}
	resources.store = store
	resources.historyStore, err = sqlite.NewChatHistory(ctx, cfg.Storage.ChatHistorySQLitePath)
	if err != nil {
		return resources, fmt.Errorf("prepare chat history: %w", err)
	}

	// 媒体侧通过 NewMediaCenter 接收完整配置并借用共享存储；
	// 后端、凭据、配置应用和中断恢复均由媒体侧负责。
	resources.media, err = media.NewMediaCenter(ctx, cfg, resources.store)
	if err != nil {
		return resources, fmt.Errorf("prepare media: %w", err)
	}
	resources.permission = security.NewPermission(cfg.Security.UserMaxToolRisk, cfg.Security.SuperadminConfirmRisk, cfg.Security.Superadmins)

	resources.models, err = modelmgr.NewModelManager(ctx, cfg)
	if err != nil {
		return resources, fmt.Errorf("prepare models: %w", err)
	}
	return resources, nil
}
