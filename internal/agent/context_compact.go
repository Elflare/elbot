package agent

import (
	"context"
	"fmt"

	"elbot/internal/contextmgr"
	"elbot/internal/modelmgr"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func (a *Agent) CompactCurrent(ctx context.Context, triggerReason string) (string, error) {
	current, err := a.sessions.Current(ctx, a.scope(ctx))
	if err != nil {
		return "", err
	}
	_, content, err := a.compactSession(ctx, current, triggerReason, a.modelSelectionForTurn(ctx, current))
	return content, err
}

func (a *Agent) compactSession(ctx context.Context, current *storage.Session, triggerReason string, fallback modelmgr.Selection) (*storage.Session, string, error) {
	next, err := a.runCompact(ctx, current, a.scope(ctx), triggerReason, fallback)
	if err != nil {
		return nil, "", err
	}
	return next, fmt.Sprintf("上下文压缩完成。\nnew session: %s", next.ID), nil
}

func (a *Agent) runCompact(ctx context.Context, current *storage.Session, scope session.Scope, triggerReason string, selection modelmgr.Selection) (*storage.Session, error) {
	locked, scope, release, err := a.enterCompact(ctx, current, scope)
	if err != nil {
		return nil, err
	}
	if len(a.requests.ListBySession(current.ID)) > 0 {
		release()
		return nil, fmt.Errorf("当前会话有正在运行的请求，无法压缩")
	}
	info, reqCtx, done, err := a.requests.Start(locked, request.StartRequest{SessionID: current.ID, Kind: request.KindCompress, Label: "compact"})
	if err != nil {
		release()
		return nil, err
	}
	if !a.turns.StartCompactRun(current.ID, info.ID, turn.ExecutionFromContext(ctx)) {
		done()
		release()
		return nil, fmt.Errorf("当前会话正在处理其他任务，无法压缩")
	}
	a.turns.AttachExecution(current.ID, info.ID, turn.ExecutionFromContext(ctx))
	release()
	defer done()
	defer a.turns.CompleteCompactRun(current.ID, info.ID)
	if err := reqCtx.Err(); err != nil {
		return nil, err
	}

	prepared, err := a.contexts.Compact(reqCtx, current, triggerReason, selection)
	if err != nil {
		return nil, err
	}
	nextID := storage.NewID()
	locked, scope, release, err = a.enterCompact(reqCtx, current, scope, nextID)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := reqCtx.Err(); err != nil {
		return nil, err
	}

	if !a.turns.CompleteCompactRun(current.ID, info.ID) {
		return nil, context.Canceled
	}
	// Carry field-owned metadata (including workspace and permanent promotion)
	// forward while replacing the compression seed.
	encoded, err := contextmgr.CompactedMetadata(current.Metadata, prepared.State)
	if err != nil {
		return nil, err
	}
	next, err := a.sessions.CreateCompacted(locked, scope, current.ID, session.CompactedRequest{ID: nextID, Title: prepared.Title, Metadata: encoded})
	if err != nil {
		return nil, err
	}
	if e := turn.ExecutionFromContext(ctx); e != nil {
		e.SetResult(next.ID, "", "")
		if !a.turns.ReserveExecution(next.ID, inboundTurnInput(ctx, ""), e) {
			return nil, session.ErrSessionBusy
		}
	}
	if e := turn.ExecutionFromContext(ctx); e != nil && e.Foreground() != nil {
		_, nextBinding, err := a.sessions.CurrentBound(locked, scope)
		if err != nil {
			return nil, err
		}
		e.Adopt(session.WithBinding(e.Foreground(), nextBinding))
	}
	return next, nil
}

func (a *Agent) enterCompact(ctx context.Context, row *storage.Session, scope session.Scope, targets ...string) (context.Context, session.Scope, func(), error) {
	for {
		if e := turn.ExecutionFromContext(ctx); e != nil && e.Foreground() != nil {
			if binding, ok := session.BindingFromContext(e.Foreground()); ok {
				ctx = session.WithBinding(ctx, binding)
				scope = binding.Scope()
			}
		}
		background := session.IsBackground(row)
		var locked context.Context
		var release func()
		var err error
		if background {
			locked, release, err = a.sessions.EnterSessions(ctx, append([]string{row.ID}, targets...)...)
		} else {
			locked, release, err = a.sessions.EnterActivation(ctx, scope, append([]string{row.ID}, targets...)...)
		}
		if err != nil {
			return ctx, scope, nil, err
		}
		latest, err := a.store.Sessions().Get(locked, row.ID)
		if err != nil {
			release()
			return ctx, scope, nil, err
		}
		*row = *latest
		if background && !session.IsBackground(row) {
			release()
			continue
		}
		if !background {
			_, binding, err := a.sessions.CurrentBound(locked, scope)
			if err != nil {
				release()
				return ctx, scope, nil, err
			}
			original, hasOriginal := session.BindingFromContext(ctx)
			if binding.SessionID() != row.ID || (hasOriginal && (original != binding || !original.Valid())) {
				release()
				return ctx, scope, nil, errSessionBindingChanged
			}
			locked = session.WithBinding(locked, binding)
		}
		return locked, scope, release, nil
	}
}

func (a *Agent) compactActive(sessionID string) bool {
	if a.turns.Snapshot(sessionID).Phase == turn.PhaseCompact {
		return true
	}
	for _, active := range a.requests.ListBySession(sessionID) {
		if active.Kind == request.KindCompress {
			return true
		}
	}
	return false
}
