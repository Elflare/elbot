package confirmation

import (
	"context"
	"elbot/internal/config"
	"time"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/operationresult"

	"elbot/internal/session"
	"elbot/internal/turn"
)

type ConfirmationIdentity interface {
	Scope(context.Context) session.Scope
	Actor(context.Context) contextinfo.Actor
}

type ConfirmationOutput interface {
	SendTo(context.Context, delivery.Target, []delivery.Output) (operationresult.Result[delivery.Receipt], error)
}

// ConfirmationCoordinatorDeps declares only the capabilities consumed by ConfirmationCoordinator.
type ConfirmationCoordinatorDeps struct {
	CommandText func(string, ...string) string // 根部绑定配置首个前缀的命令组装能力。
	Sessions    interface {
		CurrentBound(context.Context, session.Scope) (operationresult.Result[session.CurrentBoundData], error)
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
		Snapshot(string) turn.Snapshot
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
