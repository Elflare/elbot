package agent

import (
	"context"
	"errors"

	"elbot/internal/platform"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

var errSessionBindingChanged = errors.New("当前会话已切换，请重新发送消息")

// Existing execution contexts keep their original binding across confirmation.
func captureSessionBinding(ctx context.Context, sessions *session.Service, identity *identityResolver, row *storage.Session) (context.Context, error) {
	current, binding, err := sessions.CurrentBound(ctx, identity.Scope(ctx))
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

func (c *executionCoordinator) resolveInput(ctx context.Context, text string) (context.Context, *storage.Session, error) {
	locked, release, err := c.sessions.EnterScope(ctx, c.identity.Scope(ctx))
	if err != nil {
		return ctx, nil, err
	}
	defer release()
	row, err := c.sessionForInput(locked, text)
	if err != nil {
		return ctx, nil, err
	}
	locked, err = captureSessionBinding(locked, c.sessions, c.identity, row)
	return locked, row, err
}

func (c *executionCoordinator) enterTurn(ctx context.Context, row *storage.Session) (context.Context, func(), error) {
	ctx = c.view.Context(ctx)
	scope := c.identity.Scope(ctx)
	background := isBackgroundSession(row)
	var locked context.Context
	var release func()
	var err error
	if background {
		locked, release, err = c.sessions.EnterSessions(ctx, row.ID)
	} else {
		locked, release, err = c.sessions.EnterActivation(ctx, scope, row.ID)
	}
	if err != nil {
		return ctx, nil, err
	}
	latest, err := c.sessionRows.Get(locked, row.ID)
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
	refreshed := c.view.Context(locked)
	if c.identity.Scope(refreshed).Key() != scope.Key() || (background && !isBackgroundSession(row)) {
		release()
		if e := turn.ExecutionFromContext(ctx); e == nil || e.Foreground() == nil {
			return ctx, nil, errSessionBindingChanged
		}
		return c.enterTurn(refreshed, row)
	}
	if !isBackgroundSession(row) {
		locked, err = captureSessionBinding(refreshed, c.sessions, c.identity, row)
		if err != nil {
			release()
			return ctx, nil, err
		}
	}
	return locked, release, nil
}

func (c *executionCoordinator) enterInput(ctx context.Context, row *storage.Session) (context.Context, func(), error) {
	locked, release, err := c.sessions.EnterActivation(ctx, c.identity.Scope(ctx), row.ID)
	if err != nil {
		return ctx, nil, err
	}
	locked, err = captureSessionBinding(locked, c.sessions, c.identity, row)
	if err == nil {
		var latest *storage.Session
		latest, err = c.sessionRows.Get(locked, row.ID)
		if err == nil {
			switch {
			case latest.Mode != row.Mode:
				err = errors.New("当前会话模式已切换，请重新发送消息")
			case latest.ArchivedAt != nil:
				err = errInputArchived
			case c.compactActive(row.ID):
				err = errInputCompacting
			}
		}
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		release()
		return ctx, nil, err
	}
	return locked, release, nil
}

func (c *executionCoordinator) sessionForInput(ctx context.Context, text string) (*storage.Session, error) {
	if msg, ok := platform.MessageContextFrom(ctx); ok {
		if msg.ResumeSessionID != "" {
			return c.sessions.Resume(ctx, c.identity.Scope(ctx), msg.ResumeSessionID)
		}
		if msg.ForkFromMessageID != "" {
			return c.sessions.Fork(ctx, c.identity.Scope(ctx), msg.ForkFromMessageID)
		}
	}
	return c.sessions.GetOrCreateCurrent(ctx, c.identity.Scope(ctx), text)
}
