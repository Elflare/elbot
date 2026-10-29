package execution

import (
	"context"
	"elbot/internal/llm"
	"elbot/internal/signal"
	"errors"
	"log/slog"
	"time"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/contextmgr"
	"elbot/internal/modelmgr"
	"elbot/internal/request"
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
	sessions interface {
		CreateCompacted(context.Context, session.Scope, string, session.CompactedRequest) (*storage.Session, error)
		Current(context.Context, session.Scope) (*storage.Session, error)
		CurrentBound(context.Context, session.Scope) (*storage.Session, *session.Binding, error)
		EnterActivation(context.Context, session.Scope, ...string) (context.Context, func(), error)
		EnterScope(context.Context, session.Scope) (context.Context, func(), error)
		EnterSessions(context.Context, ...string) (context.Context, func(), error)
		Fork(context.Context, session.Scope, string) (*storage.Session, error)
		GetOrCreateCurrent(context.Context, session.Scope, string) (*storage.Session, error)
		MaybeScheduleNaming(context.Context, string)
		RegisterOrigin(context.Context, string, llm.Origin) (*storage.Session, error)
		Resume(context.Context, session.Scope, string) (*storage.Session, error)
		RecordActivity(context.Context, *session.Binding) error
		SessionIdle(context.Context, string) error
	}
	sessionRows interface {
		Get(context.Context, string) (*storage.Session, error)
	}
	turns interface {
		AppendPendingInput(string, turn.Input) bool
		BeginAppend(string, string, turn.Input) *turn.AppendWait
		CanCompact(string, *turn.Execution) bool
		CancelAppend(string) bool
		CompleteCompactRun(string, string) bool
		CompleteTurn(string, string) (turn.Input, bool)
		FinishAttempt(string, string)
		ReserveExecution(string, turn.Input, *turn.Execution) bool
		ResumeAppend(string) (turn.Input, *turn.Execution, bool)
		Snapshot(string) turn.Snapshot
		StartCompactRun(string, string, *turn.Execution) bool
		StartAttempt(string, turn.Input, *turn.Execution, string) bool
		StopSession(string)
		StopAttempt(string, string)
	}
	requests interface {
		CancelSession(string) int
		ListBySession(string) []request.Request
		Start(context.Context, request.StartRequest) (request.Request, context.Context, func())
	}
	contexts interface {
		Compact(context.Context, *storage.Session, string, modelmgr.Selection) (*contextmgr.PreparedCompact, error)
		ReachedCompactThreshold(context.Context, *llm.Usage, modelmgr.Selection) bool
		RecordTurn(context.Context, string, *llm.Usage) error
	}
	SelectionForTurn func(context.Context, *storage.Session) modelmgr.Selection
	TurnTimeout      string // User-facing notification for a response deadline.
	dialogue         ExecutionDialogue
	identity         interface {
		Scope(context.Context) session.Scope
		Actor(context.Context) contextinfo.Actor
	}
	view interface {
		Context(context.Context) context.Context
		WithModel(context.Context, modelmgr.Selection) (context.Context, error)
		CheckSelection(*storage.Session, modelmgr.Selection) error
	}
	output          ExecutionSender
	status          agentshare.ExecutionStatus
	waitPolicy      agentshare.ConfirmationWaitPolicy
	appendWaits     *appendWaitLifecycle
	responseTimeout time.Duration
}

func (c *ExecutionCoordinator) Run(ctx context.Context, row *storage.Session, text string, out agentshare.Output) (runErr error) {
	execution := turn.ExecutionFromContext(ctx)
	if execution == nil {
		execution = turn.NewExecution(storage.NewID())
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
	out = executionTurnOutput{view: c.view, foreground: foregroundTurnOutput{sender: c.output}, execution: execution, fallback: out}
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
