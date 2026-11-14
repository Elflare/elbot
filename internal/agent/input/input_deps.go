package input

import (
	"context"
	"log/slog"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/control"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/operationresult"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/turn"
	"elbot/internal/workspace"
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
	ResolveInput(context.Context, string) (context.Context, operationresult.Result[*storage.Session], error)
	EnterInput(context.Context, *storage.Session) (context.Context, operationresult.Result[struct{}], func(), error)
	AcceptInput(context.Context, *storage.Session, string) (context.Context, operationresult.Result[agentshare.InputDisposition], error)
	ModelContext(context.Context, *storage.Session) (context.Context, error)
	DefaultModelContext(context.Context) (context.Context, error)
	ResumeAppend(context.Context, string) (context.Context, operationresult.Result[struct{}], error)
}

type InputConfirmations interface {
	CurrentConfirmation(context.Context, control.CurrentConfirmationRequest) (operationresult.Result[control.CurrentConfirmationResult], error)
}

// InputCoordinatorDeps declares only the capabilities consumed by InputCoordinator.
type InputCoordinatorDeps struct {
	CommandText func(string, ...string) string // 根部绑定配置首个前缀的命令组装能力。
	// NormalizeInput delegates source-specific normalization after Session selection.
	NormalizeInput func(context.Context, string, []llm.MessageSegment) (operationresult.Result[[]llm.MessageSegment], error)
	// NewWorkspaceStore binds workspace operations to the admitted session.
	NewWorkspaceStore func(string) workspace.WorkspaceStore
	Sessions          interface {
		EnterActivation(context.Context, session.Scope, ...string) (context.Context, func(), error)
		Resume(context.Context, session.Scope, string) (operationresult.Result[*storage.Session], error)
		CurrentBound(context.Context, session.Scope) (operationresult.Result[session.CurrentBoundData], error)
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
