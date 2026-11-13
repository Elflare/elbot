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
		result, err := c.sessions.Fork(ctx, c.identity.Scope(ctx), msg.ForkFromMessageID)
		if err != nil {
			return ctx, operationresult.Result[*storage.Session]{}, err
		}
		if result.Status != operationresult.Success {
			return ctx, result, nil
		}
		row := result.Data
		locked, release, err := c.sessions.EnterActivation(ctx, c.identity.Scope(ctx), row.ID)
		if err != nil {
			return ctx, operationresult.Result[*storage.Session]{}, err
		}
		defer release()
		locked, captured, err := agentshare.CaptureSessionBinding(locked, c.sessions, c.identity, row)
		if err != nil {
			return ctx, operationresult.Result[*storage.Session]{}, err
		}
		if captured.Status != operationresult.Success {
			return ctx, operationresult.Result[*storage.Session]{Status: captured.Status, Message: captured.Message}, nil
		}
		return locked, result, nil
	}
	locked, release, err := c.sessions.EnterScope(ctx, c.identity.Scope(ctx))
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
	locked, captured, err := agentshare.CaptureSessionBinding(locked, c.sessions, c.identity, result.Data)
	if err != nil {
		return ctx, operationresult.Result[*storage.Session]{}, err
	}
	if captured.Status != operationresult.Success {
		return ctx, operationresult.Result[*storage.Session]{Status: captured.Status, Message: captured.Message}, nil
	}
	return locked, result, nil
}

func (c *ExecutionCoordinator) enterTurn(ctx context.Context, row *storage.Session) (context.Context, operationresult.Result[struct{}], func(), error) {
	ctx = c.view.Context(ctx)
	scope := c.identity.Scope(ctx)
	background := agentshare.IsBackgroundSession(row)
	var locked context.Context
	var release func()
	var err error
	if background {
		locked, release, err = c.sessions.EnterSessions(ctx, row.ID)
	} else {
		locked, release, err = c.sessions.EnterActivation(ctx, scope, row.ID)
	}
	if err != nil {
		return ctx, operationresult.Result[struct{}]{}, nil, err
	}
	latest, err := c.sessionRows.Get(locked, row.ID)
	if err != nil {
		release()
		return ctx, operationresult.Result[struct{}]{}, nil, err
	}
	*row = *latest
	if e := turn.ExecutionFromContext(ctx); e != nil {
		select {
		case <-e.Done():
			release()
			return ctx, operationresult.Result[struct{}]{}, nil, context.Canceled
		default:
		}
	}
	refreshed := c.view.Context(locked)
	if c.identity.Scope(refreshed).Key() != scope.Key() || (background && !agentshare.IsBackgroundSession(row)) {
		release()
		if e := turn.ExecutionFromContext(ctx); e == nil || e.Foreground() == nil {
			return ctx, operationresult.Result[struct{}]{Status: operationresult.Conflict, Message: "当前会话已切换，请重新发送消息"}, nil, nil
		}
		return c.enterTurn(refreshed, row)
	}
	if !agentshare.IsBackgroundSession(row) {
		var captured operationresult.Result[struct{}]
		locked, captured, err = agentshare.CaptureSessionBinding(refreshed, c.sessions, c.identity, row)
		if err != nil || captured.Status != operationresult.Success {
			release()
			return ctx, captured, nil, err
		}
	}
	return locked, operationresult.Result[struct{}]{Status: operationresult.Success}, release, nil
}

func (c *ExecutionCoordinator) EnterInput(ctx context.Context, row *storage.Session) (context.Context, operationresult.Result[struct{}], func(), error) {
	locked, release, err := c.sessions.EnterActivation(ctx, c.identity.Scope(ctx), row.ID)
	if err != nil {
		return ctx, operationresult.Result[struct{}]{}, nil, err
	}
	locked, captured, err := agentshare.CaptureSessionBinding(locked, c.sessions, c.identity, row)
	if err != nil || captured.Status != operationresult.Success {
		release()
		return ctx, captured, nil, err
	}
	latest, err := c.sessionRows.Get(locked, row.ID)
	if err != nil {
		release()
		return ctx, operationresult.Result[struct{}]{}, nil, err
	}
	switch {
	case latest.Mode != row.Mode:
		release()
		return ctx, operationresult.Result[struct{}]{Status: operationresult.Conflict, Message: "当前会话模式已切换，请重新发送消息"}, nil, nil
	case latest.ArchivedAt != nil:
		release()
		return ctx, operationresult.Result[struct{}]{Status: operationresult.Rejected, Message: "会话已归档，取消归档后可继续"}, nil, nil
	case c.CompactActive(row.ID) && c.turns.Snapshot(row.ID).Phase != turn.PhaseStopping:
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
			return c.sessions.Resume(ctx, c.identity.Scope(ctx), msg.ResumeSessionID)
		}
	}
	return c.sessions.GetOrCreateCurrent(ctx, c.identity.Scope(ctx), text)
}
