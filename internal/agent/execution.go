package agent

import (
	"context"
	"time"

	"elbot/internal/agent/dialogue"
	agentevents "elbot/internal/agent/events"
	"elbot/internal/contextinfo"
	"elbot/internal/contextmgr"
	"elbot/internal/modelmgr"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

// AdoptForeground is installed by app as Session's synchronous execution participant.
func (a *Agent) AdoptForeground(ctx context.Context, row *storage.Session, binding *session.Binding) {
	a.execution.AdoptForeground(ctx, row, binding)
}

func (a *Agent) CompactCurrent(ctx context.Context, triggerReason string) (string, error) {
	return a.execution.CompactCurrent(ctx, triggerReason)
}

func (c *executionCoordinator) AdoptForeground(ctx context.Context, row *storage.Session, binding *session.Binding) {
	if execution := c.turns.Execution(row.ID); execution != nil {
		ctx = contextinfo.WithActor(ctx, c.identity.Actor(ctx))
		execution.Adopt(session.WithBinding(ctx, binding))
	}
}

// executionCoordinator owns cross-turn admission and handoffs; domain managers
// remain the source of truth for requests, attempts and logical execution state.
type executionCoordinator struct {
	// Context usage diagnostics.
	sessions          *session.Service
	sessionRows       storage.SessionRepository
	turns             *turn.Manager
	requests          *request.Manager
	contexts          *contextmgr.Service
	models            *modelmgr.Service
	dialogue          *dialogue.Runner
	identity          *identityResolver
	view              dialogue.ExecutionView
	output            *outputSender
	status            *statusRecorder
	waitPolicy        *confirmationPolicy
	appendWaits       *appendWaitLifecycle
	responseTimeout   time.Duration
	persistenceFailed *signal.Signal[agentevents.PersistenceFailedEvent]
	timedOut          *signal.Signal[agentevents.TurnTimedOutEvent]
}

func (c *executionCoordinator) Run(ctx context.Context, row *storage.Session, text string, out dialogue.Output) error {
	execution := turn.ExecutionFromContext(ctx)
	if execution == nil {
		execution = turn.NewExecution(storage.NewID())
		ctx = turn.WithExecution(ctx, execution)
	}
	out = executionTurnOutput{view: c.view, sessions: c.sessions, foreground: foregroundTurnOutput{sender: c.output, status: c.status}, execution: execution, fallback: out}
	for {
		next, pending, err := c.runAttempt(ctx, row, text, out)
		if err != nil {
			execution.Finish(err)
			return err
		}
		if pending.Text == "" && len(pending.Segments) == 0 {
			if c.turns.Execution(next.ID) != execution {
				execution.Finish(nil)
			}
			return nil
		}
		ctx = c.view.Context(ctx)
		if next.ID != row.ID && !isBackgroundSession(next) {
			_, binding, err := c.sessions.CurrentBound(ctx, c.identity.Scope(ctx))
			if err != nil {
				execution.Finish(err)
				return err
			}
			if binding.SessionID() != next.ID {
				execution.Finish(errSessionBindingChanged)
				return errSessionBindingChanged
			}
			ctx = session.WithBinding(ctx, binding)
		}
		row = next
		text = pending.Text
		ctx = withInboundTurnInput(ctx, pending)
	}
}

func (c *executionCoordinator) RunBackground(ctx context.Context, row *storage.Session, text string) turn.Result {
	execution := turn.NewExecution(storage.NewID())
	execution.SetResult(row.ID, "", "")
	ctx = turn.WithExecution(ctx, execution)
	if err := c.Run(ctx, row, text, backgroundTurnOutput{status: c.status}); err != nil {
		execution.Finish(err)
	}
	result := execution.Wait(ctx)
	if latest, err := c.sessionRows.Get(context.WithoutCancel(ctx), row.ID); err == nil && session.WasPromoted(latest) {
		result.TakenOver = true
	}
	if result.Err != nil && ctx.Err() != nil {
		_, release, err := c.sessions.EnterSessions(context.WithoutCancel(ctx), result.SessionID)
		if err == nil {
			if c.turns.Execution(result.SessionID) == execution {
				c.requests.CancelSession(result.SessionID)
				c.turns.StopSession(result.SessionID)
			}
			release()
		}
	}
	return result
}
