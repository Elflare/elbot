package execution

import (
	"context"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	toolshare "elbot/internal/tool/share"
	"elbot/internal/turn"
)

type BackgroundIdentity interface {
	Actor(context.Context) contextinfo.Actor
	Permission() *security.Permission
	PlatformName() string
}

type BackgroundExecution interface {
	RunBackground(context.Context, *storage.Session, string) turn.Result
	ModelContext(context.Context, *storage.Session) (context.Context, error)
}

// BackgroundRunnerDeps declares only the capabilities consumed by BackgroundRunner.
type BackgroundRunnerDeps struct {
	Sessions    *session.Service
	SessionRows storage.SessionRepository
	Identity    BackgroundIdentity
	Execution   BackgroundExecution
	Preloader   interface {
		PrepareBackground(context.Context, toolshare.BackgroundPreloadRequest) (toolshare.BackgroundPreload, error)
	} // 借用 Tool 根部的统一预载入口。
	ToolState interface {
		Commit(context.Context, string, toolshare.StateUpdate) (toolshare.StateCommit, events.LogResult, error)
	} // 借用同一 Tool 根部的状态提交入口。
	SandboxRoot string
}
