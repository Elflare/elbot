package module

import (
	"context"

	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/elvena"
	"elbot/internal/events"
	"elbot/internal/media"
	"elbot/internal/modelmgr"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/tool/runtimeinfo"
	"elbot/internal/toolrun"
)

// ModuleDeps contains the capabilities used by background tasks, not their owners.
type ModuleDeps struct {
	ConfigPath       string
	Maintenance      config.MaintenanceConfig
	Elnis            config.ElnisConfig
	SandboxRoot      string
	Platform         config.PlatformConfig
	Superadmins      map[string][]string
	EnabledPlatforms []string
	PlatformCallers  elvena.PlatformCallerResolver
	EnableWorkers    bool // CLI-only mode still prepares tools and recovers interrupted state, but starts no scheduler or Elnis workers.
	Store            storage.Store
	ChatHistory      storage.ChatHistoryRepository
	Logs             interface {
		CleanupOldLogs() error
		RetentionDays() int
	}
	Media     *media.Manager
	Models    *modelmgr.Service
	Sessions  *session.Service
	Identity  BackgroundIdentity
	Execution BackgroundExecution
	Registry  *tool.Registry
	ToolInfo  runtimeinfo.Info
	Preloader *toolrun.PreloadService
	ToolState *toolrun.StateService
	Send      func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error)
}
