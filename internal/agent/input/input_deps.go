package input

import (
	"context"
	"log/slog"
	"time"

	"elbot/internal/agent/dialogue"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/events"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/toolrun"
	"elbot/internal/turn"
)

type InputIdentity interface {
	dialogue.Identity
	Policy() *security.Policy
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
	Sessions      *session.Service
	SessionRows   storage.SessionRepository
	Turns         *turn.Manager
	Identity      InputIdentity
	Hooks         dialogue.Hooks
	Output        InputOutput
	Execution     InputExecution
	Confirmations InputConfirmations
	Preloader     interface {
		PrepareTools(context.Context, []string) (toolrun.ToolPreload, error)
		PrepareSkills(context.Context, string, []string) (toolrun.SkillPreload, error)
	}
	ToolState interface {
		Commit(context.Context, string, toolrun.StateUpdate) (toolrun.StateCommit, events.LogResult, error)
	}
	WaitPolicy InputWaitPolicy
}
