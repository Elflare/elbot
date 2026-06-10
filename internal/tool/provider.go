package tool

import (
	"context"

	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type SchemaProvider struct {
	Registry *Registry
	Policy   *security.Policy
}

func (p SchemaProvider) Schemas(ctx context.Context, mode string, session *storage.Session, scope session.Scope) ([]llm.ToolSchema, error) {
	_ = session
	_ = scope
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if mode != storage.SessionModeWork || p.Registry == nil {
		return nil, nil
	}
	return p.Registry.Schemas(), nil
}

func (p SchemaProvider) ToolNames(ctx context.Context, mode string, session *storage.Session, scope session.Scope) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if mode != storage.SessionModeWork || p.Registry == nil {
		return nil, nil
	}
	actor := actorFromScope(ctx, scope)
	infos := p.allowedInfos(actor)
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		if info.Name != "discover_tool" && !info.Hidden {
			names = append(names, info.Name)
		}
	}
	return names, nil
}

func (p SchemaProvider) allowedInfos(actor security.Actor) []Info {
	policy := p.Policy
	if policy == nil {
		policy = security.DefaultPolicy()
	}
	infos := p.Registry.List()
	out := make([]Info, 0, len(infos))
	for _, info := range infos {
		if CanAccessTool(actor, policy, info) || info.Name == "discover_tool" {
			out = append(out, info)
		}
	}
	return out
}

func actorFromScope(ctx context.Context, scope session.Scope) security.Actor {
	if actor, ok := security.ActorFromContext(ctx); ok {
		return actor
	}
	if policy, ok := security.PolicyFromContext(ctx); ok && policy != nil {
		return policy.Actor(scope.ActorID, scope.Platform, scope.ActorID, "")
	}
	return security.Actor{ID: scope.ActorID, Platform: scope.Platform, PlatformUserID: scope.ActorID, Role: security.RoleUser}
}
