package execution

import (
	"cmp"
	"context"
	"elbot/internal/operationresult"
	"elbot/internal/signal"
	"errors"
	"log/slog"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func (c *ExecutionCoordinator) AdoptForeground(ctx context.Context, row *storage.Session, binding *session.Binding) {
	if execution := c.deps.Turns.Snapshot(row.ID).Execution; execution != nil {
		ctx = contextinfo.WithActor(ctx, c.deps.Identity.Actor(ctx))
		execution.Adopt(session.WithBinding(ctx, binding), *row)
	}
}

func (c *ExecutionCoordinator) ActiveSessionIDs() []string {
	active := make(map[string]struct{})
	for _, snapshot := range c.deps.Turns.SnapshotAll() {
		if snapshot.Phase != turn.PhaseIdle {
			active[snapshot.SessionID] = struct{}{}
		}
	}
	for _, req := range c.deps.Requests.List() {
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
	deps         ExecutionCoordinatorDeps
	attemptWaits *attemptWaitLifecycle
}

func (c *ExecutionCoordinator) Run(ctx context.Context, row *storage.Session, text string, out agentshare.Output) (requestCtx context.Context, operation operationresult.Result[struct{}], runErr error) {
	execution := turn.ExecutionFromContext(ctx)
	if execution == nil {
		var err error
		execution, err = c.deps.Turns.NewExecution(ctx)
		if err != nil {
			return ctx, operationresult.Result[struct{}]{}, err
		}
		ctx = turn.WithExecution(ctx, execution)
	}
	defer func() {
		if runErr != nil {
			operation = operationresult.Result[struct{}]{}
		}
		if runErr != nil || operation.Status != operationresult.Success {
			_, release, err := c.deps.Sessions.EnterSessions(context.WithoutCancel(ctx), row.ID)
			if err == nil {
				current := c.deps.Turns.Snapshot(row.ID)
				if current.Execution == execution && current.Attempt == "" {
					c.deps.Turns.StopAttempt(row.ID, "")
				}
				release()
			}
		}
		if snapshot := c.deps.Turns.Snapshot(row.ID); snapshot.Phase == turn.PhaseAwaitAppendConfirm {
			c.expireAppendAfter(ctx, row.ID, snapshot.AppendWaitID)
		}
		c.sessionIdle(ctx, row.ID)
	}()
	out = executionTurnOutput{view: c.deps.View, foreground: foregroundTurnOutput{ExecutionSender: c.deps.Output}, execution: execution, fallback: out}
	for {
		var input turn.Input
		var err error
		previousID := row.ID
		requestCtx, row, operation, err = c.runAttempt(ctx, row, text, out, &input)
		runErr = cmp.Or(runErr, err)
		if input.Text == "" && len(input.Segments) == 0 && len(input.Answers) == 0 && len(input.Reports) == 0 {
			if err == nil && operation.Status == operationresult.Success && c.deps.Turns.Snapshot(row.ID).Execution == execution {
				return // Another attempt owns this execution.
			}
			break
		}
		ctx = c.deps.View.Context(ctx)
		if row.ID != previousID && !agentshare.IsBackgroundSession(row) {
			current, err := c.deps.Sessions.CurrentBound(ctx, c.deps.Identity.Scope(ctx))
			if err != nil || current.Status != operationresult.Success {
				runErr = cmp.Or(runErr, err)
				operation = operationresult.Result[struct{}]{Status: current.Status, Message: current.Message}
				break
			}
			binding := current.Data.Binding
			if binding.SessionID() != row.ID {
				operation = operationresult.Result[struct{}]{Status: operationresult.Conflict, Message: "当前会话已切换，请重新发送消息"}
				break
			}
			ctx = session.WithBinding(ctx, binding)
		}
		text = input.Text
		ctx = agentshare.WithInboundTurnInput(ctx, input)
	}
	execution.Finish(runErr)
	return
}

// sessionIdle runs outside business admission, after the owning work has joined.
// Shutdown intentionally stops timers; other failures remain observable.
func (c *ExecutionCoordinator) sessionIdle(ctx context.Context, id string) {
	if err := c.deps.Sessions.SessionIdle(context.WithoutCancel(ctx), id); err != nil && !errors.Is(err, signal.ErrClosed) {
		slog.WarnContext(ctx, "session idle notification failed", "session_id", id, "error", err)
	}
}

func (c *ExecutionCoordinator) RunBackground(ctx context.Context, row *storage.Session, text string) (context.Context, operationresult.Result[turn.Result], error) {
	execution, err := c.deps.Turns.NewExecution(ctx)
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
	if result.Err != nil && ctx.Err() != nil {
		_, release, err := c.deps.Sessions.EnterSessions(context.WithoutCancel(ctx), result.SessionID)
		if err == nil {
			if c.deps.Turns.Snapshot(result.SessionID).Execution == execution {
				c.deps.Requests.CancelSession(result.SessionID)
				c.deps.Turns.StopSession(result.SessionID)
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
	return &ExecutionCoordinator{deps: deps, attemptWaits: newAttemptWaitLifecycle(ctx)}
}
