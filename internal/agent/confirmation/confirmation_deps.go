package confirmation

import (
	"context"
	"elbot/internal/config"
	"time"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/command"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"

	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

type ConfirmationIdentity interface {
	Scope(context.Context) session.Scope
	Actor(context.Context) contextinfo.Actor
}

type ConfirmationOutput interface {
	SendChat(context.Context, string) error
	SendAssistant(context.Context, string, ...delivery.Action) (delivery.Receipt, events.LogResult, error)
}

// ConfirmationCoordinatorDeps declares only the capabilities consumed by ConfirmationCoordinator.
type ConfirmationCoordinatorDeps struct {
	CommandText func(string, ...string) string // 根部绑定配置首个前缀的命令组装能力。
	Sessions    interface {
		CurrentBound(context.Context, session.Scope) (*storage.Session, *session.Binding, error)
		EnterActivation(context.Context, session.Scope, ...string) (context.Context, func(), error)
		RecordActivity(context.Context, *session.Binding) error
	}
	Requests interface {
		CancelSession(string) int
	}
	Turns interface {
		BeginRiskConfirmation(string, turn.RiskConfirmation, string) *turn.RiskConfirmationWait
		PendingRiskConfirmation(string) (turn.RiskConfirmation, bool)
		RefreshRiskConfirmation(string) bool
		ResolveRiskConfirmation(string, turn.RiskDecision) bool
		StopSession(string)
	}
	Commands interface {
		IsCommand(string) bool
		Parse(string) command.Parsed
	}
	Identity ConfirmationIdentity
	Output   ConfirmationOutput
	Policy   agentshare.ConfirmationWaitPolicy
}

// ConfirmationPolicyDeps declares only the capabilities consumed by ConfirmationPolicy.
type ConfirmationPolicyDeps struct {
	Identity                ConfirmationIdentity
	IdleExpiration          config.SessionIdleExpirationConfig
	UserConfirmationTimeout time.Duration
}
