package execution

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/operationresult"
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

func (c *ExecutionCoordinator) ActiveSessionIDs() []string {
	active := make(map[string]struct{})
	for _, snapshot := range c.turns.SnapshotAll() {
		if snapshot.Phase != turn.PhaseIdle {
			active[snapshot.SessionID] = struct{}{}
		}
	}
	for _, req := range c.requests.List() {
		if req.SessionID != "" {
			active[req.SessionID] = struct{}{}
		}
	}
	ids := make([]string, 0, len(active))
	for id := range active {
		ids = append(ids, id)
	}
	return ids
}

// ExecutionCoordinator owns cross-turn admission and handoffs; domain managers
// remain the source of truth for requests, attempts and logical execution state.
type ExecutionCoordinator struct {
	appendConfirmation delivery.Confirmation // 根部绑定当前命令前缀的追加确认展示。
	appendWait         agentshare.ConfirmationWaitPolicy
	// Context usage diagnostics.
	sessions    ExecutionSessions
	sessionRows interface {
		Get(context.Context, string) (*storage.Session, error)
	}
	turns            ExecutionTurns
	requests         ExecutionRequests
	contexts         ExecutionContexts
	SelectionForTurn func(context.Context, *storage.Session) modelmgr.Selection
	dialogue         ExecutionDialogue
	identity         ExecutionIdentity
	view             ExecutionView
	output           ExecutionSender
	status           agentshare.ExecutionStatus
	attemptWaits     *attemptWaitLifecycle
	responseTimeout  time.Duration
}

func (c *ExecutionCoordinator) Run(ctx context.Context, row *storage.Session, text string, out agentshare.Output) (requestCtx context.Context, operation operationresult.Result[struct{}], runErr error) {
	execution := turn.ExecutionFromContext(ctx)
	if execution == nil {
		var err error
		execution, err = c.turns.NewExecution(ctx)
		if err != nil {
			return ctx, operationresult.Result[struct{}]{}, err
		}
		ctx = turn.WithExecution(ctx, execution)
	}
	defer func() {
		if runErr != nil || operation.Status != operationresult.Success {
			_, release, err := c.sessions.EnterSessions(context.WithoutCancel(ctx), row.ID)
			if err == nil {
				current := c.turns.Snapshot(row.ID)
				if current.Execution == execution && current.Attempt == "" {
					c.turns.StopAttempt(row.ID, "")
				}
				release()
			}
		}
		if snapshot := c.turns.Snapshot(row.ID); snapshot.Phase == turn.PhaseAwaitAppendConfirm {
			c.expireAppendAfter(ctx, row.ID, snapshot.AppendWaitID)
		}
		c.sessionIdle(ctx, row.ID)
	}()
	out = executionTurnOutput{view: c.view, foreground: foregroundTurnOutput{ExecutionSender: c.output}, execution: execution, fallback: out}
	for {
		requestCtx, next, pending, err := c.runAttempt(ctx, row, text, out)
		previousID := row.ID
		row = next
		if err != nil {
			execution.Finish(err)
			return requestCtx, operationresult.Result[struct{}]{}, err
		}
		if pending.Status != operationresult.Success {
			execution.Finish(nil)
			return requestCtx, operationresult.Result[struct{}]{Status: pending.Status, Message: pending.Message}, nil
		}
		input := pending.Data
		if input.Text == "" && len(input.Segments) == 0 {
			if c.turns.Snapshot(next.ID).Execution != execution {
				execution.Finish(nil)
			}
			return requestCtx, operationresult.Result[struct{}]{Status: operationresult.Success}, nil
		}
		ctx = c.view.Context(ctx)
		if next.ID != previousID && !agentshare.IsBackgroundSession(next) {
			current, err := c.sessions.CurrentBound(ctx, c.identity.Scope(ctx))
			if err != nil {
				execution.Finish(err)
				return requestCtx, operationresult.Result[struct{}]{}, err
			}
			if current.Status != operationresult.Success {
				execution.Finish(nil)
				return requestCtx, operationresult.Result[struct{}]{Status: current.Status, Message: current.Message}, nil
			}
			binding := current.Data.Binding
			if binding.SessionID() != next.ID {
				execution.Finish(nil)
				return requestCtx, operationresult.Result[struct{}]{Status: operationresult.Conflict, Message: "当前会话已切换，请重新发送消息"}, nil
			}
			ctx = session.WithBinding(ctx, binding)
		}
		text = input.Text
		ctx = agentshare.WithInboundTurnInput(ctx, input)
	}
}

// sessionIdle runs outside business admission, after the owning work has joined.
// Shutdown intentionally stops timers; other failures remain observable.
func (c *ExecutionCoordinator) sessionIdle(ctx context.Context, id string) {
	if err := c.sessions.SessionIdle(context.WithoutCancel(ctx), id); err != nil && !errors.Is(err, signal.ErrClosed) {
		slog.WarnContext(ctx, "session idle notification failed", "session_id", id, "error", err)
	}
}

func (c *ExecutionCoordinator) RunBackground(ctx context.Context, row *storage.Session, text string) (context.Context, operationresult.Result[turn.Result], error) {
	execution, err := c.turns.NewExecution(ctx)
	if err != nil {
		return ctx, operationresult.Result[turn.Result]{}, err
	}
	execution.SetResult(row.ID, "", "")
	ctx = turn.WithExecution(ctx, execution)
	requestCtx, operation, err := c.Run(ctx, row, text, backgroundTurnOutput{})
	if err != nil {
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
	if result.Err != nil {
		return requestCtx, operationresult.Result[turn.Result]{}, result.Err
	}
	if operation.Status != operationresult.Success {
		return requestCtx, operationresult.Result[turn.Result]{Status: operation.Status, Message: operation.Message}, nil
	}
	return requestCtx, operationresult.Result[turn.Result]{Status: operationresult.Success, Data: result}, nil
}

// NewExecutionCoordinator initializes ExecutionCoordinator without installing cross-service callbacks.
func NewExecutionCoordinator(ctx context.Context, deps ExecutionCoordinatorDeps) *ExecutionCoordinator {
	return &ExecutionCoordinator{
		appendConfirmation: deps.AppendConfirmation,
		appendWait:         deps.AppendWait,
		sessions:           deps.Sessions,
		sessionRows:        deps.SessionRows,
		turns:              deps.Turns,
		requests:           deps.Requests,
		contexts:           deps.Contexts,
		SelectionForTurn:   deps.SelectionForTurn,
		dialogue:           deps.Dialogue,
		identity:           deps.Identity,
		view:               deps.View,
		output:             deps.Output,
		status:             deps.Status,
		responseTimeout:    deps.ResponseTimeout,
		attemptWaits:       newAttemptWaitLifecycle(ctx),
	}
}
