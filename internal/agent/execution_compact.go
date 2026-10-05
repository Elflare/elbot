package agent

import (
	"context"
	"fmt"

	"elbot/internal/contextmgr"
	"elbot/internal/modelmgr"
	"elbot/internal/request"
	sessionpkg "elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func (c *executionCoordinator) CompactCurrent(ctx context.Context, triggerReason string) (string, error) {
	current, err := c.sessions.Current(ctx, c.identity.Scope(ctx))
	if err != nil {
		return "", err
	}
	_, content, err := c.compactSession(ctx, current, triggerReason, modelSelectionForTurn(ctx, c.models, current))
	return content, err
}

func (c *executionCoordinator) compactSession(ctx context.Context, current *storage.Session, triggerReason string, fallback modelmgr.Selection) (*storage.Session, string, error) {
	next, err := c.runCompact(ctx, current, c.identity.Scope(ctx), triggerReason, fallback)
	if err != nil {
		return nil, "", err
	}
	return next, fmt.Sprintf("上下文压缩完成。\nnew session: %s", next.ID), nil
}

func (c *executionCoordinator) runCompact(ctx context.Context, current *storage.Session, scope sessionpkg.Scope, triggerReason string, selection modelmgr.Selection) (*storage.Session, error) {
	locked, scope, release, err := c.enterCompact(ctx, current, scope)
	if err != nil {
		return nil, err
	}
	if len(c.requests.ListBySession(current.ID)) > 0 {
		release()
		return nil, fmt.Errorf("当前会话有正在运行的请求，无法压缩")
	}
	info, reqCtx, done, err := c.requests.Start(locked, request.StartRequest{SessionID: current.ID, Kind: request.KindCompress, Label: "compact"})
	if err != nil {
		release()
		return nil, err
	}
	if !c.turns.StartCompactRun(current.ID, info.ID, turn.ExecutionFromContext(ctx)) {
		done()
		release()
		return nil, fmt.Errorf("当前会话正在处理其他任务，无法压缩")
	}
	c.turns.AttachExecution(current.ID, info.ID, turn.ExecutionFromContext(ctx))
	release()
	defer done()
	defer c.turns.CompleteCompactRun(current.ID, info.ID)
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
	next, err := c.sessions.CreateCompacted(locked, scope, current.ID, sessionpkg.CompactedRequest{ID: nextID, Title: prepared.Title, Metadata: encoded})
	if err != nil {
		return nil, err
	}
	if e := turn.ExecutionFromContext(ctx); e != nil {
		e.SetResult(next.ID, "", "")
		if !c.turns.ReserveExecution(next.ID, inboundTurnInput(ctx, ""), e) {
			return nil, sessionpkg.ErrSessionBusy
		}
	}
	if e := turn.ExecutionFromContext(ctx); e != nil && e.Foreground() != nil {
		_, nextBinding, err := c.sessions.CurrentBound(locked, scope)
		if err != nil {
			return nil, err
		}
		e.Adopt(sessionpkg.WithBinding(e.Foreground(), nextBinding))
	}
	return next, nil
}

func (c *executionCoordinator) enterCompact(ctx context.Context, row *storage.Session, scope sessionpkg.Scope, targets ...string) (context.Context, sessionpkg.Scope, func(), error) {
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
				return ctx, scope, nil, errSessionBindingChanged
			}
			locked = sessionpkg.WithBinding(locked, binding)
		}
		return locked, scope, release, nil
	}
}

func compactActive(turns *turn.Manager, requests *request.Manager, sessionID string) bool {
	if turns.Snapshot(sessionID).Phase == turn.PhaseCompact {
		return true
	}
	for _, active := range requests.ListBySession(sessionID) {
		if active.Kind == request.KindCompress {
			return true
		}
	}
	return false
}

func (c *executionCoordinator) compactActive(sessionID string) bool {
	return compactActive(c.turns, c.requests, sessionID)
}

func (c *executionCoordinator) compactBeforeTurn(ctx context.Context, session *storage.Session, text string, out turnOutput, selection modelmgr.Selection) (context.Context, *storage.Session, error) {
	if c.turns.CanCompact(session.ID, turn.ExecutionFromContext(ctx)) && c.shouldCompact(ctx, session, selection) {
		next, content, err := c.compactSession(withInboundTurnInput(ctx, inboundTurnInput(ctx, text)), session, "auto", selection)
		if err != nil {
			return ctx, session, err
		}
		session = next
		ctx = c.view.Context(ctx)
		if !isBackgroundSession(session) {
			_, binding, err := c.sessions.CurrentBound(ctx, c.identity.Scope(ctx))
			if err != nil {
				return ctx, session, err
			}
			ctx = sessionpkg.WithBinding(ctx, binding)
		}
		_, _ = out.SendAssistant(ctx, content)
	}
	return ctx, session, nil
}
