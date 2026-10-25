package background

import (
	"context"

	"elbot/internal/background/execution"
	"elbot/internal/delivery"
	"elbot/internal/elvena"
	"elbot/internal/events"
	"elbot/internal/media"
	"elbot/internal/modelmgr"
	"elbot/internal/session"
	"elbot/internal/storage"
	toolshare "elbot/internal/tool/share"
)

// TODO: Sessions、Identity、Execution、ChatHistory、PlatformCallers 和 Send 待公开能力契约确认后收口。
// SupervisorDeps contains the capabilities used by background tasks, not their owners.
type SupervisorDeps struct {
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
	Identity  execution.BackgroundIdentity
	Execution execution.BackgroundExecution
	Preloader interface {
		PrepareBackground(context.Context, toolshare.BackgroundPreloadRequest) (toolshare.BackgroundPreload, error)
		BackgroundSelections(context.Context, []string) ([]toolshare.BackgroundSelection, error)
	} // 由 Tool 根部提供后台预载与授权前的声明展开。
	ToolState interface {
		Commit(context.Context, string, toolshare.StateUpdate) (toolshare.StateCommit, events.LogResult, error)
	} // 由同一 Tool 根部提供状态提交。
	Send func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error)
}
