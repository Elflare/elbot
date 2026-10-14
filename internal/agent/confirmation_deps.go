package agent

import (
	"context"
	"time"

	"elbot/internal/command"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/turn"
)

type ConfirmationIdentity interface {
	Scope(context.Context) session.Scope
	Actor(context.Context) contextinfo.Actor
}

type ConfirmationOutput interface {
	SendChat(context.Context, string) error
	SendAssistant(context.Context, string) (delivery.Receipt, events.LogResult, error)
}

// ConfirmationCoordinatorDeps declares only the capabilities consumed by ConfirmationCoordinator.
type ConfirmationCoordinatorDeps struct {
	Sessions *session.Service
	Requests *request.Manager
	Turns    *turn.Manager
	Commands *command.Router
	Identity ConfirmationIdentity
	Output   ConfirmationOutput
	Policy   ConfirmationWaitPolicy
}

// ConfirmationPolicyDeps declares only the capabilities consumed by ConfirmationPolicy.
type ConfirmationPolicyDeps struct {
	Identity                ConfirmationIdentity
	IdleExpiration          session.IdleExpirationConfig
	UserConfirmationTimeout time.Duration
}
