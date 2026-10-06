package dialogue

import (
	"context"

	"elbot/internal/chatinfo"
	"elbot/internal/config"
	"elbot/internal/modelmgr"
	"elbot/internal/platform"
	sandboxctx "elbot/internal/sandbox"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

// ExecutionView reads the existing execution identity; it owns no adoption state.
type ExecutionView struct {
	Sessions storage.SessionRepository
}

// Rebuild semantic values on the request context, retaining its cancellation.
func (v ExecutionView) Context(ctx context.Context) context.Context {
	e := turn.ExecutionFromContext(ctx)
	if e == nil {
		return ctx
	}
	foreground := e.Foreground()
	if foreground == nil {
		return ctx
	}
	// Replace the entire routing snapshot, including an absent platform context.
	// Local CLI supplies only Info; inheriting the background discard sender
	// would silently lose every subsequent foreground output.
	msg, _ := platform.MessageContextFrom(foreground)
	if info, ok := chatinfo.FromContext(foreground); ok {
		msg.Info = info
	}
	ctx = platform.WithMessageContext(ctx, msg)
	if actor, ok := security.ActorFromContext(foreground); ok {
		ctx = security.WithActor(ctx, actor)
	}
	if binding, ok := session.BindingFromContext(foreground); ok {
		ctx = session.WithBinding(ctx, binding)
	}
	ctx = sandboxctx.WithSandboxContext(ctx, sandboxctx.SandboxContext{})
	ctx = modelmgr.WithSelectionOverride(ctx, config.ModelSelection{})
	return ctx
}

func (v ExecutionView) RefreshSession(ctx context.Context, row *storage.Session) (context.Context, error) {
	ctx = v.Context(ctx)
	latest, err := v.Sessions.Get(ctx, row.ID)
	if err != nil {
		return ctx, err
	}
	*row = *latest
	return ctx, nil
}
