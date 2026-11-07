package input

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/command"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/workspace"
	"log/slog"

	"elbot/internal/turn"
)

type InputIdentity interface {
	Scope(context.Context) session.Scope
	Actor(context.Context) contextinfo.Actor
}

type InputOutput interface {
	Send(context.Context, delivery.StreamEvent) (delivery.Receipt, error)
	Notify(context.Context, slog.Level, string)
}

type InputExecution interface {
	ResolveInput(context.Context, string) (context.Context, *storage.Session, error)
	EnterInput(context.Context, *storage.Session) (context.Context, func(), error)
	AcceptInput(context.Context, *storage.Session, string) (context.Context, agentshare.InputDisposition, error)
	ModelContext(context.Context, *storage.Session) (context.Context, error)
	ResumeAppend(context.Context, string) (context.Context, error)
}

type InputConfirmations interface {
	SubmitResponse(context.Context, string, string) (*command.Result, error)
}

// InputCoordinatorDeps declares only the capabilities consumed by InputCoordinator.
type InputCoordinatorDeps struct {
	CommandText func(string, ...string) string // 根部绑定配置首个前缀的命令组装能力。
	// NewWorkspaceStore binds workspace operations to the admitted session.
	NewWorkspaceStore func(string) workspace.WorkspaceStore
	Sessions          interface {
		EnterActivation(context.Context, session.Scope, ...string) (context.Context, func(), error)
		Resume(context.Context, session.Scope, string) (*storage.Session, error)
		CurrentBound(context.Context, session.Scope) (*storage.Session, *session.Binding, error)
		Current(context.Context, session.Scope) (*storage.Session, error)
		RecordActivity(context.Context, *session.Binding) error
	}
	Turns    interface{ Snapshot(string) turn.Snapshot }
	Identity InputIdentity
	Hooks    interface {
		AgentInputPrepared(context.Context, agentshare.Session, agentshare.Message) ([]llm.MessageSegment, error)
	}
	Output        InputOutput
	Execution     InputExecution
	Confirmations InputConfirmations
	Preloader     interface {
		PrepareTools(context.Context, []string) (tool.ToolPreload, error)
		PrepareSkills(context.Context, string, []string) (tool.SkillPreload, error)
	}
	ToolState interface {
		Commit(context.Context, string, tool.StateUpdate) (tool.StateCommit, error)
	}
}
