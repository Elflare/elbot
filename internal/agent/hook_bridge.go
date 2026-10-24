package agent

import (
	"context"
	"elbot/internal/agent/hooks"
	"elbot/internal/contextinfo"
	"elbot/internal/request"
	"strings"
)

// RegisterHooks accepts one provider. Detaching does not interrupt callbacks
// already running under their original contexts.
func (m *Supervisor) RegisterHooks(points hooks.Points) (func(), error) {
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
func (m *Supervisor) StartHookRequest(ctx context.Context, sessionID, name string) (context.Context, func()) {
	_, runCtx, done := m.requests.Start(ctx, request.StartRequest{
		ParentID:  contextinfo.RootRequestIDFromContext(ctx),
		SessionID: strings.TrimSpace(sessionID),
		Kind:      request.KindHook,
		Label:     strings.TrimSpace(name),
	})
	return runCtx, done
}
