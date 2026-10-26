package input

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/workspace"
	"log/slog"
	"time"

	"elbot/internal/turn"
)

type InputIdentity interface {
	Scope(context.Context) session.Scope
	Actor(context.Context) contextinfo.Actor
}

type InputOutput interface {
	SendChat(context.Context, string) error
	Notify(context.Context, slog.Level, string)
}

type InputExecution interface {
	ResolveInput(context.Context, string) (context.Context, *storage.Session, error)
	EnterInput(context.Context, *storage.Session) (context.Context, func(), error)
	AcceptInput(context.Context, *storage.Session, string) (agentshare.InputDisposition, error)
	ModelContext(context.Context, *storage.Session) (context.Context, error)
}

type InputConfirmations interface {
	SubmitResponse(context.Context, string, string) error
}

type InputWaitPolicy interface {
	WaitTimeout(context.Context) time.Duration
	IdleExpiration() session.IdleExpirationConfig
}

// InputCoordinatorDeps declares only the capabilities consumed by InputCoordinator.
type InputCoordinatorDeps struct {
	// NewWorkspaceStore binds workspace operations to the admitted session.
	NewWorkspaceStore func(string) workspace.WorkspaceStore
	Sessions          interface {
		EnterActivation(context.Context, session.Scope, ...string) (context.Context, func(), error)
		Resume(context.Context, session.Scope, string) (*storage.Session, error)
		CurrentBound(context.Context, session.Scope) (*storage.Session, *session.Binding, error)
		Current(context.Context, session.Scope) (*storage.Session, error)
		ExpireIdleCurrent(context.Context, session.ExpireIdleRequest) (session.ExpireIdleResult, error)
	}
	SessionRows interface {
		Get(context.Context, string) (*storage.Session, error)
	}
	Turns    interface{ Snapshot(string) turn.Snapshot }
	Identity InputIdentity
	Hooks    interface {
		AgentInputPrepared(context.Context, agentshare.Session, agentshare.Message) ([]llm.MessageSegment, events.LogResult, error)
	}
	Output        InputOutput
	Execution     InputExecution
	Confirmations InputConfirmations
	Preloader     interface {
		PrepareTools(context.Context, []string) (tool.ToolPreload, error)
		PrepareSkills(context.Context, string, []string) (tool.SkillPreload, error)
	}
	ToolState interface {
		Commit(context.Context, string, tool.StateUpdate) (tool.StateCommit, events.LogResult, error)
	}
	WaitPolicy InputWaitPolicy
}
