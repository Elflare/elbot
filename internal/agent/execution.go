package agent

import (
	"context"
	"fmt"
	"time"

	"elbot/internal/agent/dialogue"
	"elbot/internal/contextinfo"
	"elbot/internal/contextmgr"
	"elbot/internal/modelmgr"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

// AdoptForeground is installed by app as Session's synchronous execution participant.

func (c *ExecutionCoordinator) AdoptForeground(ctx context.Context, row *storage.Session, binding *session.Binding) {
	if execution := c.turns.Execution(row.ID); execution != nil {
		ctx = contextinfo.WithActor(ctx, c.identity.Actor(ctx))
		execution.Adopt(session.WithBinding(ctx, binding))
	}
}

// ExecutionCoordinator owns cross-turn admission and handoffs; domain managers
// remain the source of truth for requests, attempts and logical execution state.
type ExecutionCoordinator struct {
	// Context usage diagnostics.
	sessions        *session.Service
	sessionRows     storage.SessionRepository
	turns           *turn.Manager
	requests        *request.Manager
	contexts        *contextmgr.Service
	models          *modelmgr.Service
	dialogue        ExecutionDialogue
	identity        dialogue.Identity
	view            dialogue.ExecutionView
	output          ExecutionSender
	status          ExecutionStatus
	waitPolicy      ConfirmationWaitPolicy
	appendWaits     *appendWaitLifecycle
	responseTimeout time.Duration
}

func (c *ExecutionCoordinator) Run(ctx context.Context, row *storage.Session, text string, out dialogue.Output) error {
	execution := turn.ExecutionFromContext(ctx)
	if execution == nil {
		execution = turn.NewExecution(storage.NewID())
		ctx = turn.WithExecution(ctx, execution)
	}
	out = executionTurnOutput{view: c.view, sessions: c.sessions, foreground: foregroundTurnOutput{sender: c.output}, execution: execution, fallback: out}
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

func (c *ExecutionCoordinator) RunBackground(ctx context.Context, row *storage.Session, text string) turn.Result {
	execution := turn.NewExecution(storage.NewID())
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
			if c.turns.Execution(result.SessionID) == execution {
				c.requests.CancelSession(result.SessionID)
				c.turns.StopSession(result.SessionID)
			}
			release()
		}
	}
	return result
}

// NewExecutionCoordinator initializes ExecutionCoordinator without installing cross-service callbacks.
func NewExecutionCoordinator(ctx context.Context, deps ExecutionCoordinatorDeps) (*ExecutionCoordinator, error) {
	if ctx == nil {
		return nil, fmt.Errorf("runtime context is required")
	}
	if deps.Sessions == nil {
		return nil, fmt.Errorf("ExecutionCoordinator sessions is required")
	}
	if deps.SessionRows == nil {
		return nil, fmt.Errorf("ExecutionCoordinator sessionRows is required")
	}
	if deps.Turns == nil {
		return nil, fmt.Errorf("ExecutionCoordinator turns is required")
	}
	if deps.Requests == nil {
		return nil, fmt.Errorf("ExecutionCoordinator requests is required")
	}
	if deps.Contexts == nil {
		return nil, fmt.Errorf("ExecutionCoordinator contexts is required")
	}
	if deps.Models == nil {
		return nil, fmt.Errorf("ExecutionCoordinator models is required")
	}
	if deps.Dialogue == nil {
		return nil, fmt.Errorf("ExecutionCoordinator dialogue is required")
	}
	if deps.Identity == nil {
		return nil, fmt.Errorf("ExecutionCoordinator identity is required")
	}
	if deps.Output == nil {
		return nil, fmt.Errorf("ExecutionCoordinator output is required")
	}
	if deps.Status == nil {
		return nil, fmt.Errorf("ExecutionCoordinator status is required")
	}
	if deps.WaitPolicy == nil {
		return nil, fmt.Errorf("ExecutionCoordinator waitPolicy is required")
	}
	return &ExecutionCoordinator{
		sessions:        deps.Sessions,
		sessionRows:     deps.SessionRows,
		turns:           deps.Turns,
		requests:        deps.Requests,
		contexts:        deps.Contexts,
		models:          deps.Models,
		dialogue:        deps.Dialogue,
		identity:        deps.Identity,
		view:            deps.View,
		output:          deps.Output,
		status:          deps.Status,
		waitPolicy:      deps.WaitPolicy,
		responseTimeout: deps.ResponseTimeout,
		appendWaits:     newAppendWaitLifecycle(ctx),
	}, nil
}
