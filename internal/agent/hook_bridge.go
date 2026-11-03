package agent

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/request"
	"elbot/internal/signal"
	"errors"
	"log/slog"
	"strings"
	"sync"
)

// RegisterHooks accepts one provider. Detaching does not interrupt callbacks
// already running under their original contexts.
func (m *Supervisor) RegisterHooks(points Points) (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, context.Canceled
	}
	return m.bridge.Register(points)
}

// StartHookRequest tracks one actual handler, not an entire callback group.
// Empty session IDs are valid before input binding. Provider-global work that
// does not participate in Agent execution must not use this entrypoint.
func (m *Supervisor) StartHookRequest(ctx context.Context, sessionID, name string) (context.Context, func(), error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID != "" {
		locked, release, err := m.sessions.EnterSessions(ctx, sessionID)
		if err != nil {
			return ctx, nil, err
		}
		ctx = locked
		defer release()
	}
	_, runCtx, done, err := m.requests.Start(ctx, request.StartRequest{
		ParentID:  contextinfo.RootRequestIDFromContext(ctx),
		SessionID: sessionID,
		Kind:      request.KindHook,
		Label:     strings.TrimSpace(name),
	})
	if err != nil {
		return ctx, nil, err
	}
	return runCtx, sync.OnceFunc(func() {
		done()
		if sessionID != "" {
			if err := m.sessions.SessionIdle(context.WithoutCancel(ctx), sessionID); err != nil && !errors.Is(err, signal.ErrClosed) {
				slog.WarnContext(ctx, "hook session idle notification failed", "session_id", sessionID, "error", err)
			}
		}
	}), nil
}
