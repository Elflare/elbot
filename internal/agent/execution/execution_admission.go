package execution

import (
	"context"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/operationresult"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func (c *ExecutionCoordinator) ResolveInput(ctx context.Context, text string) (context.Context, operationresult.Result[*storage.Session], error) {
	// Fork prepares native/media material outside scope admission. Capture the
	// newly activated binding afterward, with the same final identity check.
	if msg, ok := agentshare.MessageContextFrom(ctx); ok && msg.ResumeSessionID == "" && msg.ForkFromMessageID != "" {
		result, err := c.deps.Sessions.Fork(ctx, c.deps.Identity.Scope(ctx), msg.ForkFromMessageID)
		if err != nil {
			return ctx, operationresult.Result[*storage.Session]{}, err
		}
		if result.Status != operationresult.Success {
			return ctx, result, nil
		}
		row := result.Data
		locked, release, err := c.deps.Sessions.EnterActivation(ctx, c.deps.Identity.Scope(ctx), row.ID)
		if err != nil {
			return ctx, operationresult.Result[*storage.Session]{}, err
		}
		defer release()
		bound, captured, err := agentshare.CaptureSessionBinding(locked, c.deps.Sessions, c.deps.Identity, row.ID)
		if err != nil || captured.Status != operationresult.Success {
			return ctx, captured, err
		}
		return bound, captured, nil
	}
	locked, release, err := c.deps.Sessions.EnterScope(ctx, c.deps.Identity.Scope(ctx))
	if err != nil {
		return ctx, operationresult.Result[*storage.Session]{}, err
	}
	defer release()
	result, err := c.sessionForInput(locked, text)
	if err != nil {
		return ctx, operationresult.Result[*storage.Session]{}, err
	}
	if result.Status != operationresult.Success {
		return ctx, result, nil
	}
	bound, captured, err := agentshare.CaptureSessionBinding(locked, c.deps.Sessions, c.deps.Identity, result.Data.ID)
	if err != nil || captured.Status != operationresult.Success {
		return ctx, captured, err
	}
	return bound, captured, nil
}

func (c *ExecutionCoordinator) enterTurn(ctx context.Context, row *storage.Session) (context.Context, operationresult.Result[struct{}], func(), error) {
	// Re-read under the selected gate: promotion can commit while admission waits.
	execution := turn.ExecutionFromContext(ctx)
	checkDone := func() error {
		select {
		case <-execution.Done():
			return context.Canceled
		default:
			return nil
		}
	}
	if agentshare.IsBackgroundSession(row) {
		locked, release, err := c.deps.Sessions.EnterSessions(ctx, row.ID)
		if err != nil {
			return ctx, operationresult.Result[struct{}]{}, nil, err
		}
		if err := checkDone(); err != nil {
			release()
			return ctx, operationresult.Result[struct{}]{}, nil, err
		}
		latest, err := c.deps.SessionRows.Get(locked, row.ID)
		if err != nil {
			release()
			return ctx, operationresult.Result[struct{}]{}, nil, err
		}
		*row = *latest
		if agentshare.IsBackgroundSession(row) {
			return locked, operationresult.Result[struct{}]{Status: operationresult.Success}, release, nil
		}
		release()
		if execution.Foreground() == nil {
			return ctx, operationresult.Result[struct{}]{Status: operationresult.Conflict, Message: "当前会话已切换，请重新发送消息"}, nil, nil
		}
	}
	ctx = c.deps.View.Context(ctx)
	scope := c.deps.Identity.Scope(ctx)
	locked, release, err := c.deps.Sessions.EnterActivation(ctx, scope, row.ID)
	if err != nil {
		return ctx, operationresult.Result[struct{}]{}, nil, err
	}
	if err := checkDone(); err != nil {
		release()
		return ctx, operationresult.Result[struct{}]{}, nil, err
	}
	refreshed := c.deps.View.Context(locked)
	if c.deps.Identity.Scope(refreshed).Key() != scope.Key() {
		release()
		return ctx, operationresult.Result[struct{}]{Status: operationresult.Conflict, Message: "当前会话已切换，请重新发送消息"}, nil, nil
	}
	bound, captured, err := agentshare.CaptureSessionBinding(refreshed, c.deps.Sessions, c.deps.Identity, row.ID)
	if err != nil || captured.Status != operationresult.Success {
		release()
		return ctx, operationresult.Result[struct{}]{Status: captured.Status, Message: captured.Message}, nil, err
	}
	*row = *captured.Data
	return bound, operationresult.Result[struct{}]{Status: operationresult.Success}, release, nil
}

func (c *ExecutionCoordinator) EnterInput(ctx context.Context, row *storage.Session) (context.Context, operationresult.Result[struct{}], func(), error) {
	locked, release, err := c.deps.Sessions.EnterActivation(ctx, c.deps.Identity.Scope(ctx), row.ID)
	if err != nil {
		return ctx, operationresult.Result[struct{}]{}, nil, err
	}
	locked, captured, err := agentshare.CaptureSessionBinding(locked, c.deps.Sessions, c.deps.Identity, row.ID)
	if err != nil || captured.Status != operationresult.Success {
		release()
		return ctx, operationresult.Result[struct{}]{Status: captured.Status, Message: captured.Message}, nil, err
	}
	latest := captured.Data
	switch {
	case latest.Mode != row.Mode:
		release()
		return ctx, operationresult.Result[struct{}]{Status: operationresult.Conflict, Message: "当前会话模式已切换，请重新发送消息"}, nil, nil
	case latest.ArchivedAt != nil:
		release()
		return ctx, operationresult.Result[struct{}]{Status: operationresult.Rejected, Message: "会话已归档，取消归档后可继续"}, nil, nil
	case c.CompactActive(row.ID) && c.deps.Turns.Snapshot(row.ID).Phase != turn.PhaseStopping:
		release()
		return ctx, operationresult.Result[struct{}]{Status: operationresult.Busy, Message: "compacting · 请等待压缩完成，或停止当前请求"}, nil, nil
	}
	if err := ctx.Err(); err != nil {
		release()
		return ctx, operationresult.Result[struct{}]{}, nil, err
	}
	return locked, operationresult.Result[struct{}]{Status: operationresult.Success}, release, nil
}

func (c *ExecutionCoordinator) sessionForInput(ctx context.Context, text string) (operationresult.Result[*storage.Session], error) {
	if msg, ok := agentshare.MessageContextFrom(ctx); ok {
		if msg.ResumeSessionID != "" {
			return c.deps.Sessions.Resume(ctx, c.deps.Identity.Scope(ctx), msg.ResumeSessionID)
		}
	}
	return c.deps.Sessions.GetOrCreateCurrent(ctx, c.deps.Identity.Scope(ctx), text)
}
