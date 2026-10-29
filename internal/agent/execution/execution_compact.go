package execution

import (
	"context"
	"fmt"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextmgr"
	"elbot/internal/modelmgr"
	"elbot/internal/request"
	sessionpkg "elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func (c *ExecutionCoordinator) CompactCurrent(ctx context.Context, triggerReason string) (string, error) {
	current, err := c.sessions.Current(ctx, c.identity.Scope(ctx))
	if err != nil {
		return "", err
	}
	_, content, err := c.compactSession(ctx, current, triggerReason, c.SelectionForTurn(ctx, current))
	return content, err
}

func (c *ExecutionCoordinator) compactSession(ctx context.Context, current *storage.Session, triggerReason string, fallback modelmgr.Selection) (*storage.Session, string, error) {
	next, err := c.runCompact(ctx, current, c.identity.Scope(ctx), triggerReason, fallback)
	if err != nil {
		return nil, "", err
	}
	return next, fmt.Sprintf("上下文压缩完成。\nnew session: %s", next.ID), nil
}

func (c *ExecutionCoordinator) runCompact(ctx context.Context, current *storage.Session, scope sessionpkg.Scope, triggerReason string, selection modelmgr.Selection) (*storage.Session, error) {
	locked, scope, release, err := c.enterCompact(ctx, current, scope)
	if err != nil {
		return nil, err
	}
	if len(c.requests.ListBySession(current.ID)) > 0 {
		release()
		return nil, fmt.Errorf("当前会话有正在运行的请求，无法压缩")
	}
	if err := c.view.CheckSelection(current, selection); err != nil {
		release()
		return nil, err
	}
	info, reqCtx, done := c.requests.Start(locked, request.StartRequest{SessionID: current.ID, Kind: request.KindCompress, Label: "compact"})
	if !c.turns.StartCompactRun(current.ID, info.ID, turn.ExecutionFromContext(ctx)) {
		done()
		release()
		return nil, fmt.Errorf("当前会话正在处理其他任务，无法压缩")
	}
	reqCtx = turn.WithAttempt(reqCtx, info.ID)
	release()
	defer func() {
		done()
		c.turns.CompleteCompactRun(current.ID, info.ID)
		c.sessionIdle(ctx, current.ID)
	}()
	stopObserver := c.observeRequestStop(reqCtx, current.ID, info.ID)
	defer stopObserver()
	if err := reqCtx.Err(); err != nil {
		return nil, err
	}

	prepared, err := c.contexts.Compact(reqCtx, current, triggerReason, selection)
	if err != nil {
		return nil, err
	}
	nextID := storage.NewID()
	locked, scope, release, err = c.enterCompact(reqCtx, current, scope, nextID)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := reqCtx.Err(); err != nil {
		return nil, err
	}

	if !c.turns.CompleteCompactRun(current.ID, info.ID) {
		return nil, context.Canceled
	}
	// Carry field-owned metadata (including workspace and permanent promotion)
	// forward while replacing the compression seed.
	encoded, err := contextmgr.CompactedMetadata(current.Metadata, prepared.State)
	if err != nil {
		return nil, err
	}
	next, err := c.sessions.CreateCompacted(locked, scope, current.ID, sessionpkg.CompactedRequest{ID: nextID, Title: prepared.Title, Metadata: encoded, Seed: prepared.Seed, ExpectedCheckpointID: prepared.ExpectedCheckpointID})
	if err != nil {
		return nil, err
	}
	if e := turn.ExecutionFromContext(ctx); e != nil {
		e.SetResult(next.ID, "", "")
		if !c.turns.ReserveExecution(next.ID, agentshare.InboundTurnInput(ctx, ""), e) {
			return nil, fmt.Errorf("上下文压缩已完成，但新会话已有其他执行，无法继续当前处理")
		}
	}
	if e := turn.ExecutionFromContext(ctx); e != nil && e.Foreground() != nil {
		_, nextBinding, err := c.sessions.CurrentBound(locked, scope)
		if err != nil {
			return nil, err
		}
		e.Adopt(sessionpkg.WithBinding(e.Foreground(), nextBinding), *next)
	}
	return next, nil
}

func (c *ExecutionCoordinator) enterCompact(ctx context.Context, row *storage.Session, scope sessionpkg.Scope, targets ...string) (context.Context, sessionpkg.Scope, func(), error) {
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
			locked, release, err = c.sessions.EnterSessions(ctx, append([]string{row.ID}, targets...)...)
		} else {
			locked, release, err = c.sessions.EnterActivation(ctx, scope, append([]string{row.ID}, targets...)...)
		}
		if err != nil {
			return ctx, scope, nil, err
		}
		latest, err := c.sessionRows.Get(locked, row.ID)
		if err != nil {
			release()
			return ctx, scope, nil, err
		}
		*row = *latest
		if background && !sessionpkg.IsBackground(row) {
			release()
			continue
		}
		if !background {
			_, binding, err := c.sessions.CurrentBound(locked, scope)
			if err != nil {
				release()
				return ctx, scope, nil, err
			}
			original, hasOriginal := sessionpkg.BindingFromContext(ctx)
			if binding.SessionID() != row.ID || (hasOriginal && (original != binding || !original.Valid())) {
				release()
				return ctx, scope, nil, agentshare.ErrSessionBindingChanged
			}
			locked = sessionpkg.WithBinding(locked, binding)
		}
		return locked, scope, release, nil
	}
}

func (c *ExecutionCoordinator) CompactActive(sessionID string) bool {
	return agentshare.CompactActive(c.turns, c.requests, sessionID)
}

func (c *ExecutionCoordinator) compactBeforeTurn(ctx context.Context, session *storage.Session, text string, out agentshare.Output, selection modelmgr.Selection) (context.Context, *storage.Session, error) {
	if c.turns.CanCompact(session.ID, turn.ExecutionFromContext(ctx)) && c.shouldCompact(ctx, session, selection) {
		next, content, err := c.compactSession(agentshare.WithInboundTurnInput(ctx, agentshare.InboundTurnInput(ctx, text)), session, "auto", selection)
		if err != nil {
			return ctx, session, err
		}
		session = next
		ctx = c.view.Context(ctx)
		if !agentshare.IsBackgroundSession(session) {
			_, binding, err := c.sessions.CurrentBound(ctx, c.identity.Scope(ctx))
			if err != nil {
				return ctx, session, err
			}
			ctx = sessionpkg.WithBinding(ctx, binding)
		}
		_, _, _ = out.SendAssistant(ctx, content)
	}
	return ctx, session, nil
}
