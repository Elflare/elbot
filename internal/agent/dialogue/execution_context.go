package dialogue

import (
	"context"
	"fmt"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/modelmgr"
	"elbot/internal/platform"
	sandboxctx "elbot/internal/sandbox"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

// ExecutionView reads the existing execution identity; it owns no adoption state.
type ExecutionView struct {
	Sessions storage.SessionRepository
}

func (v ExecutionView) CheckSelection(row *storage.Session, selection modelmgr.Selection) error {
	source, _, err := session.Origin(row)
	if err != nil {
		return err
	}
	return modelmgr.CanSwitch(source, selection.Origin)
}

func (v ExecutionView) WithModel(ctx context.Context, selection modelmgr.Selection) (context.Context, error) {
	if selection.Client == nil {
		return ctx, fmt.Errorf("model provider %q not found", selection.Provider)
	}
	return contextinfo.WithModel(ctx, contextinfo.Model{Provider: selection.Provider, Model: selection.Model, APIType: string(selection.Origin.APIType)}), nil
}

// Rebuild semantic values on the request context, retaining its cancellation.
func (v ExecutionView) Context(ctx context.Context) context.Context {
	e := turn.ExecutionFromContext(ctx)
	if e == nil {
		return ctx
	}
	return foregroundContext(ctx, e.Foreground())
}

func foregroundContext(ctx, foreground context.Context) context.Context {
	if foreground == nil {
		return ctx
	}
	// Replace the entire routing snapshot, including an absent platform context.
	// Local CLI has no message sender; inheriting a background discard sender
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

// RefreshExecution applies only the committed handoff facts at a tool-call
// boundary. Tool state written since adoption remains owned by the caller.
func (v ExecutionView) RefreshExecution(ctx context.Context, row *storage.Session) (context.Context, error) {
	e := turn.ExecutionFromContext(ctx)
	if e == nil {
		return ctx, nil
	}
	foreground, latest := e.ForegroundState()
	if foreground == nil {
		return ctx, nil
	}
	ctx = foregroundContext(ctx, foreground)
	if row.Mode == latest.Mode && session.WasPromoted(row) {
		return ctx, nil
	}
	fields, err := storage.DecodeSessionMetadata(row.Metadata)
	if err != nil {
		return ctx, err
	}
	promoted, err := storage.DecodeSessionMetadata(latest.Metadata)
	if err != nil {
		return ctx, err
	}
	fields["foreground_origin"] = promoted["foreground_origin"]
	delete(fields, "background_kind")
	metadata, err := fields.Encode()
	if err != nil {
		return ctx, err
	}
	row.OwnerID, row.Platform, row.PlatformScopeID = latest.OwnerID, latest.Platform, latest.PlatformScopeID
	row.Mode, row.Metadata = latest.Mode, metadata
	return ctx, nil
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
