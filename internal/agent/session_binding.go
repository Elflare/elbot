package agent

import (
	"context"
	"errors"

	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

var errSessionBindingChanged = errors.New("当前会话已切换，请重新发送消息")

// Existing execution contexts keep their original binding across confirmation.
func (a *Agent) captureSessionBinding(ctx context.Context, row *storage.Session) (context.Context, error) {
	current, binding, err := a.sessions.CurrentBound(ctx, a.scope(ctx))
	if err != nil {
		return ctx, err
	}
	if current.ID != row.ID {
		return ctx, errSessionBindingChanged
	}
	if original, ok := session.BindingFromContext(ctx); ok {
		if original != binding || !original.Valid() {
			return ctx, errSessionBindingChanged
		}
		return ctx, nil
	}
	return session.WithBinding(ctx, binding), nil
}

func (a *Agent) resolveInput(ctx context.Context, text string) (context.Context, *storage.Session, error) {
	locked, release, err := a.sessions.EnterScope(ctx, a.scope(ctx))
	if err != nil {
		return ctx, nil, err
	}
	defer release()
	row, err := a.sessionForInput(locked, text)
	if err != nil {
		return ctx, nil, err
	}
	locked, err = a.captureSessionBinding(locked, row)
	return locked, row, err
}

func (a *Agent) enterTurn(ctx context.Context, row *storage.Session, out turnOutput) (context.Context, func(), error) {
	ctx = a.executionContext(ctx)
	scope := a.scope(ctx)
	background := isBackgroundSession(row)
	var locked context.Context
	var release func()
	var err error
	if background {
		locked, release, err = a.sessions.EnterSessions(ctx, row.ID)
	} else {
		locked, release, err = a.sessions.EnterActivation(ctx, scope, row.ID)
	}
	if err != nil {
		return ctx, nil, err
	}
	latest, err := a.store.Sessions().Get(locked, row.ID)
	if err != nil {
		release()
		return ctx, nil, err
	}
	*row = *latest
	if e := turn.ExecutionFromContext(ctx); e != nil {
		select {
		case <-e.Done():
			release()
			return ctx, nil, context.Canceled
		default:
		}
	}
	refreshed := a.executionContext(locked)
	if a.scope(refreshed).Key() != scope.Key() || (background && !isBackgroundSession(row)) {
		release()
		if e := turn.ExecutionFromContext(ctx); e == nil || e.Foreground() == nil {
			return ctx, nil, errSessionBindingChanged
		}
		return a.enterTurn(refreshed, row, out)
	}
	if !isBackgroundSession(row) {
		locked, err = a.captureSessionBinding(refreshed, row)
		if err != nil {
			release()
			return ctx, nil, err
		}
	}
	return locked, release, nil
}
