package execution

import (
	"context"
	"elbot/internal/signal"
	"errors"
	"log/slog"
	"time"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/modelmgr"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func (c *ExecutionCoordinator) AdoptForeground(ctx context.Context, row *storage.Session, binding *session.Binding) {
	if execution := c.turns.Snapshot(row.ID).Execution; execution != nil {
		ctx = contextinfo.WithActor(ctx, c.identity.Actor(ctx))
		execution.Adopt(session.WithBinding(ctx, binding), *row)
	}
}

// ExecutionCoordinator owns cross-turn admission and handoffs; domain managers
// remain the source of truth for requests, attempts and logical execution state.
type ExecutionCoordinator struct {
	// Context usage diagnostics.
	sessions    ExecutionSessions
	sessionRows interface {
		Get(context.Context, string) (*storage.Session, error)
	}
	turns            ExecutionTurns
	requests         ExecutionRequests
	contexts         ExecutionContexts
	SelectionForTurn func(context.Context, *storage.Session) modelmgr.Selection
	TurnTimeout      string // User-facing notification for a response deadline.
	dialogue         ExecutionDialogue
	identity         ExecutionIdentity
	view             ExecutionView
	output           ExecutionSender
	status           agentshare.ExecutionStatus
	waitPolicy       agentshare.ConfirmationWaitPolicy
	appendWaits      *appendWaitLifecycle
	responseTimeout  time.Duration
}

func (c *ExecutionCoordinator) Run(ctx context.Context, row *storage.Session, text string, out agentshare.Output) (runErr error) {
	execution := turn.ExecutionFromContext(ctx)
	if execution == nil {
		var err error
		execution, err = c.turns.NewExecution(ctx)
		if err != nil {
			return err
		}
		ctx = turn.WithExecution(ctx, execution)
	}
	defer func() {
		// An error before StartAttempt must also retire a reservation inherited
		// from append confirmation or compaction. Never stop another execution.
		if runErr != nil {
			_, release, err := c.sessions.EnterSessions(context.WithoutCancel(ctx), row.ID)
			if err == nil {
				current := c.turns.Snapshot(row.ID)
				if current.Execution == execution && current.Attempt == "" {
					c.turns.StopAttempt(row.ID, "")
				}
				release()
			}
		}
		c.sessionIdle(ctx, row.ID)
	}()
	out = executionTurnOutput{view: c.view, foreground: foregroundTurnOutput{ExecutionSender: c.output}, execution: execution, fallback: out}
	for {
		next, pending, err := c.runAttempt(ctx, row, text, out)
		previousID := row.ID
		row = next
		if err != nil {
			execution.Finish(err)
			return err
		}
		if pending.Text == "" && len(pending.Segments) == 0 {
			if c.turns.Snapshot(next.ID).Execution != execution {
				execution.Finish(nil)
			}
			return nil
		}
		ctx = c.view.Context(ctx)
		if next.ID != previousID && !agentshare.IsBackgroundSession(next) {
			_, binding, err := c.sessions.CurrentBound(ctx, c.identity.Scope(ctx))
			if err != nil {
				execution.Finish(err)
				return err
			}
			if binding.SessionID() != next.ID {
				execution.Finish(agentshare.ErrSessionBindingChanged)
				return agentshare.ErrSessionBindingChanged
			}
			ctx = session.WithBinding(ctx, binding)
		}
		text = pending.Text
		ctx = agentshare.WithInboundTurnInput(ctx, pending)
	}
}

// sessionIdle runs outside business admission, after the owning work has joined.
// Shutdown intentionally stops timers; other failures remain observable.
func (c *ExecutionCoordinator) sessionIdle(ctx context.Context, id string) {
	if err := c.sessions.SessionIdle(context.WithoutCancel(ctx), id); err != nil && !errors.Is(err, signal.ErrClosed) {
		slog.WarnContext(ctx, "session idle notification failed", "session_id", id, "error", err)
	}
}

func (c *ExecutionCoordinator) RunBackground(ctx context.Context, row *storage.Session, text string) turn.Result {
	execution, err := c.turns.NewExecution(ctx)
	if err != nil {
		return turn.Result{SessionID: row.ID, Err: err, Outcome: "failed"}
	}
	execution.SetResult(row.ID, "", "")
	ctx = turn.WithExecution(ctx, execution)
	if err := c.Run(ctx, row, text, backgroundTurnOutput{}); err != nil {
		execution.Finish(err)
	}
	result := execution.Wait(ctx)
	if latest, err := c.sessionRows.Get(context.WithoutCancel(ctx), row.ID); err == nil && session.WasPromoted(latest) {
		result.TakenOver = true
	}
	if result.Err != nil && ctx.Err() != nil {
		_, release, err := c.sessions.EnterSessions(context.WithoutCancel(ctx), result.SessionID)
		if err == nil {
			if c.turns.Snapshot(result.SessionID).Execution == execution {
				c.requests.CancelSession(result.SessionID)
				c.turns.StopSession(result.SessionID)
			}
			release()
		}
	}
	return result
}

// NewExecutionCoordinator initializes ExecutionCoordinator without installing cross-service callbacks.
func NewExecutionCoordinator(ctx context.Context, deps ExecutionCoordinatorDeps) *ExecutionCoordinator {
	return &ExecutionCoordinator{
		sessions:         deps.Sessions,
		sessionRows:      deps.SessionRows,
		turns:            deps.Turns,
		requests:         deps.Requests,
		contexts:         deps.Contexts,
		SelectionForTurn: deps.SelectionForTurn,
		TurnTimeout:      deps.TurnTimeout,
		dialogue:         deps.Dialogue,
		identity:         deps.Identity,
		view:             deps.View,
		output:           deps.Output,
		status:           deps.Status,
		waitPolicy:       deps.WaitPolicy,
		responseTimeout:  deps.ResponseTimeout,
		appendWaits:      newAppendWaitLifecycle(ctx),
	}
}
