package app

import (
	"errors"
	"log/slog"

	agentevents "elbot/internal/agent/events"
	"elbot/internal/signal"
)

func (b *signalBindings) connectAgentLogs(events agentevents.Signals, runtime, audit *slog.Logger) error {
	runtimeQueue, err := b.newQueue("agent.runtime_logs", true)
	if err != nil {
		return err
	}
	auditQueue, err := b.newQueue("agent.audit_logs", true)
	if err != nil {
		return err
	}
	r := signal.ConnectOptions{Executor: runtimeQueue, Lifetime: signal.FollowExecutor, Shutdown: signal.Drain}
	a := signal.ConnectOptions{Executor: auditQueue, Lifetime: signal.FollowExecutor, Shutdown: signal.Drain}
	l := agentLogger{runtime: runtime, audit: audit}
	return errors.Join(
		connectSignal(b, events.UserInputReceived, l.userInput, r),
		connectSignal(b, events.PersistenceFailed, l.persistence, a),
		connectSignal(b, events.TurnTimedOut, l.timeoutOutput, r),
		connectSignal(b, events.TurnTimedOut, l.timeoutAudit, a),
		connectSignal(b, events.ModelCallCompleted, l.modelOutput, r),
		connectSignal(b, events.ModelCallCompleted, l.modelAudit, a),
		connectSignal(b, events.ToolCallCompleted, l.toolOutput, r),
		connectSignal(b, events.ToolCallCompleted, l.toolAudit, a),
		connectSignal(b, events.ConfirmationChanged, l.confirmation, a),
		connectSignal(b, events.ToolDenied, l.denied, a),
		connectSignal(b, events.HookFailed, l.hookFailure, r),
		connectSignal(b, events.ReplyDelivered, l.deliveryOutput, r),
		connectSignal(b, events.ReplyDelivered, l.deliveryAudit, a),
		connectSignal(b, events.ReplyCommitted, l.commitOutput, r),
		connectSignal(b, events.ReplyCommitted, l.commitAudit, a),
	)
}
