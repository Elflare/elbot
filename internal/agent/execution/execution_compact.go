package execution

import (
	"context"
	"elbot/internal/delivery"
	"errors"
	"time"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextmgr"
	"elbot/internal/modelmgr"
	"elbot/internal/operationresult"
	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
	sessionpkg "elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func (c *ExecutionCoordinator) CompactCurrent(ctx context.Context, triggerReason string) (operationresult.Result[*storage.Session], error) {
	current, err := c.deps.Sessions.Current(ctx, c.deps.Identity.Scope(ctx))
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	if current.Status != operationresult.Success {
		return current, nil
	}
	row := current.Data
	return c.runCompact(ctx, row, c.deps.Identity.Scope(ctx), triggerReason, c.deps.SelectionForTurn(ctx, row))
}

func (c *ExecutionCoordinator) runCompact(ctx context.Context, current *storage.Session, scope sessionpkg.Scope, triggerReason string, selection modelmgr.Selection) (result operationresult.Result[*storage.Session], runErr error) {
	entered, scope, release, err := c.enterCompact(ctx, current, scope)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	if entered.Status != operationresult.Success {
		return operationresult.Result[*storage.Session]{Status: entered.Status, Message: entered.Message}, nil
	}
	locked := entered.Data
	if len(c.deps.Requests.ListBySession(current.ID)) > 0 {
		release()
		return operationresult.Result[*storage.Session]{Status: operationresult.Busy, Message: "会话有运行中的请求，暂不能压缩"}, nil
	}
	compatible, err := c.deps.View.CheckSelection(current, selection)
	if err != nil {
		release()
		return operationresult.Result[*storage.Session]{}, err
	}
	if compatible.Status != operationresult.Success {
		release()
		return operationresult.Result[*storage.Session]{Status: compatible.Status, Message: compatible.Message}, nil
	}
	info, reqCtx, done, err := c.deps.Requests.Start(locked, request.StartRequest{SessionID: current.ID, Kind: request.KindCompress, Label: "compact"})
	if err != nil {
		release()
		return operationresult.Result[*storage.Session]{}, err
	}
	if !c.deps.Turns.StartCompactRun(current.ID, info.ID, turn.ExecutionFromContext(ctx)) {
		done()
		release()
		return operationresult.Result[*storage.Session]{Status: operationresult.Busy, Message: "会话忙，暂不能压缩"}, nil
	}
	reqCtx = turn.WithAttempt(reqCtx, info.ID)
	release()
	status := c.deps.Status.Snapshot(current.ID)
	status.SessionID, status.Phase = current.ID, runtimestatus.PhaseCompact
	status.Provider, status.Model, status.Mode = selection.Provider, selection.Model, current.Mode
	status.RequestID, status.Kind, status.Label = info.ID, info.Kind, info.Label
	status.ToolName, status.Error, status.FinishedAt = "", "", time.Time{}
	if status.TurnStartedAt.IsZero() {
		status.TurnStartedAt = info.StartedAt
	}
	status.StageStartedAt = info.StartedAt
	c.deps.Status.Record(reqCtx, status)
	defer func() {
		status := c.deps.Status.Snapshot(current.ID)
		status.Phase = runtimestatus.PhaseDone
		status.RequestID, status.Kind, status.Label, status.ToolName = "", "", "", ""
		status.StageStartedAt = status.TurnStartedAt
		status.FinishedAt = time.Now()
		status.Error = ""
		if runErr != nil && !errors.Is(runErr, context.Canceled) && !errors.Is(runErr, context.DeadlineExceeded) {
			status.Phase, status.Error = runtimestatus.PhaseError, runErr.Error()
		} else if result.Status != "" && result.Status != operationresult.Success && result.Status != operationresult.Canceled {
			status.Phase, status.Error = runtimestatus.PhaseError, result.Message
			if status.Error == "" {
				status.Error = string(result.Status)
			}
		}
		c.deps.Status.Record(context.WithoutCancel(reqCtx), status)
		done()
		c.deps.Turns.CompleteCompactRun(current.ID, info.ID)
		c.sessionIdle(ctx, current.ID)
	}()
	stopObserver := c.observeRequestStop(reqCtx, current.ID, info.ID)
	defer stopObserver()
	if err := reqCtx.Err(); err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	prepared, err := c.deps.Contexts.Compact(reqCtx, current, triggerReason, selection)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	if prepared.Status != operationresult.Success {
		return operationresult.Result[*storage.Session]{Status: prepared.Status, Message: prepared.Message}, nil
	}
	nextID, err := c.deps.Sessions.AllocateID(reqCtx, current)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	entered, scope, release, err = c.enterCompact(reqCtx, current, scope, nextID)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	if entered.Status != operationresult.Success {
		return operationresult.Result[*storage.Session]{Status: entered.Status, Message: entered.Message}, nil
	}
	locked = entered.Data
	defer release()
	if err := reqCtx.Err(); err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	if !c.deps.Turns.CompleteCompactRun(current.ID, info.ID) {
		return operationresult.Result[*storage.Session]{Status: operationresult.Canceled, Message: "上下文压缩已取消"}, nil
	}
	// Carry field-owned metadata forward while replacing the compression seed.
	data := prepared.Data
	encoded, err := contextmgr.CompactedMetadata(current.Metadata, data.State)
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	created, err := c.deps.Sessions.CreateCompacted(locked, scope, current.ID, sessionpkg.CompactedRequest{ID: nextID, Title: data.Title, Metadata: encoded, InitialMessage: data.InitialMessage, Seed: data.Seed, ExpectedCheckpointID: data.ExpectedCheckpointID})
	if err != nil {
		return operationresult.Result[*storage.Session]{}, err
	}
	if created.Status != operationresult.Success {
		return created, nil
	}
	next := created.Data
	if e := turn.ExecutionFromContext(ctx); e != nil {
		e.SetResult(next.ID, "", "")
		if !c.deps.Turns.ReserveExecution(next.ID, agentshare.InboundTurnInput(ctx, ""), e) {
			return operationresult.Result[*storage.Session]{Status: operationresult.Conflict, Message: "上下文压缩已完成，但新会话已有其他执行，无法继续当前处理"}, nil
		}
	}
	if e := turn.ExecutionFromContext(ctx); e != nil && e.Foreground() != nil {
		current, err := c.deps.Sessions.CurrentBound(locked, scope)
		if err != nil {
			return operationresult.Result[*storage.Session]{}, err
		}
		if current.Status != operationresult.Success {
			return operationresult.Result[*storage.Session]{Status: current.Status, Message: current.Message}, nil
		}
		e.Adopt(sessionpkg.WithBinding(e.Foreground(), current.Data.Binding), *next)
	}
	return created, nil
}

func (c *ExecutionCoordinator) enterCompact(ctx context.Context, row *storage.Session, scope sessionpkg.Scope, targets ...string) (operationresult.Result[context.Context], sessionpkg.Scope, func(), error) {
	for {
		if e := turn.ExecutionFromContext(ctx); e != nil && e.Foreground() != nil {
			if binding, ok := sessionpkg.BindingFromContext(e.Foreground()); ok {
				ctx = sessionpkg.WithBinding(ctx, binding)
				scope = binding.Scope()
			}
		}
		background := sessionpkg.IsBackground(row)
		var locked context.Context
		var release func()
		var err error
		if background {
			locked, release, err = c.deps.Sessions.EnterSessions(ctx, append([]string{row.ID}, targets...)...)
		} else {
			locked, release, err = c.deps.Sessions.EnterActivation(ctx, scope, append([]string{row.ID}, targets...)...)
		}
		if err != nil {
			return operationresult.Result[context.Context]{}, scope, nil, err
		}
		latest, err := c.deps.SessionRows.GetSession(locked, row.ID)
		if err != nil {
			release()
			return operationresult.Result[context.Context]{}, scope, nil, err
		}
		*row = *latest
		if background && !sessionpkg.IsBackground(row) {
			release()
			continue
		}
		if !background {
			current, err := c.deps.Sessions.CurrentBound(locked, scope)
			if err != nil {
				release()
				return operationresult.Result[context.Context]{}, scope, nil, err
			}
			if current.Status != operationresult.Success {
				release()
				return operationresult.Result[context.Context]{Status: current.Status, Message: current.Message}, scope, nil, nil
			}
			binding := current.Data.Binding
			original, hasOriginal := sessionpkg.BindingFromContext(ctx)
			if binding.SessionID() != row.ID || (hasOriginal && (original != binding || !original.Valid())) {
				release()
				return operationresult.Result[context.Context]{Status: operationresult.Conflict, Message: "原会话绑定已经失效"}, scope, nil, nil
			}
			locked = sessionpkg.WithBinding(locked, binding)
		}
		return operationresult.Result[context.Context]{Status: operationresult.Success, Data: locked}, scope, release, nil
	}
}

func (c *ExecutionCoordinator) CompactActive(sessionID string) bool {
	return agentshare.CompactActive(c.deps.Turns, c.deps.Requests, sessionID)
}

func (c *ExecutionCoordinator) compactBeforeTurn(ctx context.Context, session *storage.Session, text string, out agentshare.Output, selection modelmgr.Selection) (context.Context, operationresult.Result[*storage.Session], error) {
	if c.deps.Turns.CanCompact(session.ID, turn.ExecutionFromContext(ctx)) && c.shouldCompact(ctx, session, selection) {
		compactCtx := agentshare.WithInboundTurnInput(ctx, agentshare.InboundTurnInput(ctx, text))
		next, err := c.runCompact(compactCtx, session, c.deps.Identity.Scope(compactCtx), "auto", selection)
		if err != nil || next.Status != operationresult.Success {
			return ctx, next, err
		}
		session = next.Data
		ctx = c.deps.View.Context(ctx)
		if !agentshare.IsBackgroundSession(session) {
			current, err := c.deps.Sessions.CurrentBound(ctx, c.deps.Identity.Scope(ctx))
			if err != nil {
				return ctx, operationresult.Result[*storage.Session]{}, err
			}
			if current.Status != operationresult.Success {
				return ctx, operationresult.Result[*storage.Session]{Status: current.Status, Message: current.Message}, nil
			}
			ctx = sessionpkg.WithBinding(ctx, current.Data.Binding)
		}
		_, _ = out.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: "compacted → " + session.ID})
	}
	return ctx, operationresult.Result[*storage.Session]{Status: operationresult.Success, Data: session}, nil
}
