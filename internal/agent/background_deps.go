package agent

import (
	"context"

	"elbot/internal/agent/dialogue"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/toolrun"
	"elbot/internal/turn"
)

type BackgroundIdentity interface {
	dialogue.Identity
	Policy() *security.Policy
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
	Preloader   *toolrun.PreloadService
	ToolState   *toolrun.StateService
	SandboxRoot string
}
