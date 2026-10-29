package app

import (
	"context"

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
	media        *media.MediaCenter       // 共享媒体服务，借用媒体存储与历史查询。
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
		return resources, err
	}
	resources.config = cfg
	resources.logs, err = logging.NewManager(cfg.Runtime.LogLevel, cfg.Storage.SessionsSQLitePath, cfg.Runtime.LogRetentionDays)
	if err != nil {
		return resources, &lifecycleError{component: componentLogs, phase: phaseBuild, cause: err}
	}
	store, err := sqlite.New(ctx, cfg.Storage.SessionsSQLitePath)
	if err != nil {
		return resources, &lifecycleError{component: componentStorage, phase: phaseBuild, cause: err}
	}
	resources.store = store
	resources.historyStore, err = sqlite.NewChatHistory(ctx, cfg.Storage.ChatHistorySQLitePath)
	if err != nil {
		return resources, &lifecycleError{component: componentChatHistory, phase: phaseBuild, cause: err}
	}

	// 媒体侧接收完整配置及借用依赖，负责后端、凭据、恢复与首次历史对账。
	resources.media, err = media.NewMediaCenter(ctx, cfg, media.MediaCenterDeps{
		Store: resources.store, ChatHistory: resources.historyStore.Repository(),
	})
	if err != nil {
		return resources, &lifecycleError{component: componentMedia, phase: phaseBuild, cause: err}
	}
	resources.permission = security.NewPermission(cfg.Security.UserMaxToolRisk, cfg.Security.SuperadminConfirmRisk, cfg.Security.Superadmins)

	resources.models, err = modelmgr.NewModelManager(ctx, cfg)
	if err != nil {
		return resources, &lifecycleError{component: componentModels, phase: phaseBuild, cause: err}
	}
	return resources, nil
}
