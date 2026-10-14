package agent

import (
	"context"
	"time"

	"elbot/internal/agent/dialogue"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
	"elbot/internal/turn"
)

type InputIdentity interface {
	dialogue.Identity
	Policy() *security.Policy
}

type InputOutput interface {
	SendChat(context.Context, string) error
}

type InputExecution interface {
	ResolveInput(context.Context, string) (context.Context, *storage.Session, error)
	EnterInput(context.Context, *storage.Session) (context.Context, func(), error)
	AcceptInput(context.Context, *storage.Session, string) (InputDisposition, error)
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
	Registry      *tool.Registry
	Preloader     *toolrun.PreloadService
	ToolState     *toolrun.StateService
	WaitPolicy    InputWaitPolicy
}
