package dialogue

import (
	"context"
	"fmt"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/platform"
	sandboxctx "elbot/internal/sandbox"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

// ExecutionView reads the existing execution identity; it owns no adoption state.
type ExecutionView struct {
	Sessions  storage.SessionRepository
	Providers ProviderIdentityResolver
}

type ProviderIdentityResolver interface {
	OriginFor(string) (llm.Origin, error)
}

func (v ExecutionView) CheckSelection(row *storage.Session, selection modelmgr.Selection) error {
	source, _, err := session.Origin(row)
	if err != nil {
		return err
	}
	target, err := v.Providers.OriginFor(selection.Provider)
	if err != nil {
		return err
	}
	return modelmgr.CanSwitch(source, target)
}

func (v ExecutionView) WithModel(ctx context.Context, selection modelmgr.Selection) (context.Context, error) {
	if v.Providers == nil {
		return ctx, fmt.Errorf("provider identity bindings are not configured")
	}
	origin, err := v.Providers.OriginFor(selection.Provider)
	if err != nil {
		return ctx, err
	}
	return contextinfo.WithModel(ctx, contextinfo.Model{Provider: selection.Provider, Model: selection.Model, Protocol: string(origin.Protocol)}), nil
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
	// Local CLI supplies only public facts; inheriting a background discard sender
	// would silently lose every subsequent foreground output.
	if msg, ok := platform.MessageContextFrom(foreground); ok {
		ctx = platform.WithMessageContext(ctx, msg)
	} else {
		ctx = platform.WithoutMessageContext(ctx)
	}
	if info, ok := contextinfo.ConversationFromContext(foreground); ok {
		ctx = contextinfo.WithConversation(ctx, info)
	} else {
		ctx = contextinfo.WithoutConversation(ctx)
	}
	if actor, ok := contextinfo.ActorFromContext(foreground); ok {
		ctx = contextinfo.WithActor(ctx, actor)
	} else {
		ctx = contextinfo.WithoutActor(ctx)
	}
	if binding, ok := session.BindingFromContext(foreground); ok {
		ctx = session.WithBinding(ctx, binding)
	} else {
		ctx = session.WithBinding(ctx, nil)
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
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	facts.SessionID = row.ID
	ctx = contextinfo.WithExecution(ctx, facts)
	return ctx, nil
}
