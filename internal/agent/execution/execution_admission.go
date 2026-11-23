package execution

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/session"
	"math"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/operationresult"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func (c *ExecutionCoordinator) ResolveInput(ctx context.Context, text string) (context.Context, operationresult.Result[*storage.Session], error) {
	info, _ := contextinfo.ConversationFromContext(ctx)
	private := info.CoagentMainSessionID != ""
	msg, ok := agentshare.MessageContextFrom(ctx)
	fork := !private && ok && msg.ResumeSessionID == "" && msg.ForkFromMessageID != ""
	scope := c.deps.Identity.Scope(ctx)
	var result operationresult.Result[*storage.Session]
	var locked context.Context
	var release func()
	var err error
	if fork {
		// Fork prepares native/media material before entering activation.
		result, err = c.deps.Sessions.Fork(ctx, scope, msg.ForkFromMessageID)
		if err != nil {
			return ctx, operationresult.Result[*storage.Session]{}, err
		}
		if result.Status != operationresult.Success {
			return ctx, result, nil
		}
		locked, release, err = c.deps.Sessions.EnterActivation(ctx, scope, result.Data.ID)
	} else {
		locked, release, err = c.deps.Sessions.EnterScope(ctx, scope)
	}
	if err != nil {
		return ctx, operationresult.Result[*storage.Session]{}, err
	}
	defer release()
	if !fork {
		if private { // 显式成员视图不根据引用切换/派生或创建当前会话。
			result, err = c.deps.Sessions.Current(locked, scope)
		} else {
			result, err = c.sessionForInput(locked, text)
		}
		if err != nil {
			return ctx, operationresult.Result[*storage.Session]{}, err
		}
		if result.Status != operationresult.Success {
			return ctx, result, nil
		}
	}
	if (result.Data.TaskType == "coagent") != private {
		return ctx, operationresult.Result[*storage.Session]{Status: operationresult.Rejected, Message: "请在主会话的留言板联系协作成员"}, nil
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
	if info, _ := contextinfo.ConversationFromContext(locked); info.CoagentMainSessionID != "" {
		binding, _ := session.BindingFromContext(locked)
		board, err := c.deps.Sessions.ReadCoagentBoard(locked, binding, math.MaxInt64)
		if err != nil {
			release()
			return ctx, operationresult.Result[struct{}]{}, nil, err
		}
		if board.Status != operationresult.Success || board.Data.MainSessionID != info.CoagentMainSessionID || captured.Data.ID == board.Data.MainSessionID {
			release()
			return ctx, operationresult.Result[struct{}]{Status: operationresult.Forbidden, Message: "成员已不在当前协作组，请重新选择"}, nil, nil
		}
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
