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
)

// sharedResources 保存成功构造的共享资源。
type sharedResources struct {
	config     *config.Config         // 已加载的完整配置。
	logs       *logging.Manager       // App 持有的日志管理。
	store      *storage.Supervisor    // 两库在 Media 构造前就绪，完整启动后交由 Storage 关闭。
	media      *media.MediaCenter     // 共享媒体服务，借用媒体存储与历史查询。
	permission *security.Permission   // 共享权限管理。
	models     *modelmgr.ModelManager // 拥有模型选择状态、厂商快照与重试信号。
}

// prepareResources 只消费已定位路径；失败直接返回错误，由进程退出回收资源。
func prepareResources(ctx context.Context, resolvedConfigPath string) (*sharedResources, error) {
	cfg, err := config.Load(resolvedConfigPath)
	if err != nil {
		return nil, err
	}
	resources := &sharedResources{config: cfg}
	resources.logs, err = logging.NewManager(cfg.Runtime.LogLevel, cfg.Storage.SessionsSQLitePath, cfg.Runtime.LogRetentionDays)
	if err != nil {
		return nil, fmt.Errorf("logs build: %w", err)
	}
	resources.store, err = storage.NewSupervisor(ctx, cfg.Storage.SessionsSQLitePath, cfg.Storage.ChatHistorySQLitePath)
	if err != nil {
		return nil, fmt.Errorf("storage build: %w", err)
	}

	// 媒体侧接收完整配置及借用依赖，负责后端、凭据、恢复与首次历史对账。
	resources.media, err = media.NewMediaCenter(ctx, cfg, media.MediaCenterDeps{
		Store: resources.store, ChatHistory: resources.store,
	})
	if err != nil {
		return nil, fmt.Errorf("media build: %w", err)
	}
	resources.permission = security.NewPermission(cfg.Security.UserMaxToolRisk, cfg.Security.SuperadminConfirmRisk, cfg.Security.Superadmins)

	resources.models, err = modelmgr.NewModelManager(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("models build: %w", err)
	}
	return resources, nil
}
