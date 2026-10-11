package agent

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/contextinfo"
	"elbot/internal/platform"
	"elbot/internal/security"
	"elbot/internal/session"
)

// IdentityResolver owns entry defaults and the policy used to resolve actors.
// SourceActor deliberately does not apply entry defaults to source-free Hooks.
type IdentityResolver struct {
	platformName string
	actorID      string
	scopeID      string
	policy       *security.Policy
}

// Scope resolves the same execution identity for commands and message handling.

func (r *IdentityResolver) Scope(ctx context.Context) session.Scope {
	actor := r.Actor(ctx)
	platformName := r.platformName
	scopeID := r.scopeID
	kind := contextinfo.ConversationUnknown
	if info, ok := contextinfo.ConversationFromContext(ctx); ok {
		kind = info.Source.ConversationKind
		if info.Source.Platform != "" {
			platformName = info.Source.Platform
		}
		if info.Source.ScopeID != "" {
			scopeID = info.Source.ScopeID
		}
	}
	return session.Scope{
		ConversationKind: kind,
		ActorID:          actor.ID,
		Platform:         platformName,
		PlatformScopeID:  scopeID,
		IsCLI:            platformName == "cli" && actor.Role == contextinfo.RoleSuperadmin,
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func (r *IdentityResolver) Actor(ctx context.Context) contextinfo.Actor {
	if actor, ok := contextinfo.ActorFromContext(ctx); ok && (actor.ID != "" || actor.Role != "") {
		return actor
	}
	platformName := r.platformName
	platformUserID := r.actorID
	identity := contextinfo.Identity{}
	if info, ok := contextinfo.ConversationFromContext(ctx); ok {
		if info.Source.Platform != "" {
			platformName = info.Source.Platform
		}
		if info.Identity.PlatformUserID != "" {
			platformUserID = info.Identity.PlatformUserID
		}
		identity = info.Identity
	}
	groupRole := contextinfo.GroupRoleUnknown
	if msg, ok := platform.MessageContextFrom(ctx); ok {
		groupRole = security.ParseGroupRole(string(msg.GroupRole))
	}
	if prefix := platformName + ":"; strings.HasPrefix(platformUserID, prefix) {
		platformUserID = strings.TrimPrefix(platformUserID, prefix)
	}
	policy := r.policy
	if policy == nil {
		policy = security.DefaultPolicy()
	}
	actor := policy.Actor(identity.ActorID, platformName, platformUserID, identity.DisplayName)
	actor.Nickname = identity.Nickname
	actor.GroupCard = identity.GroupCard
	actor.GroupRole = groupRole
	return actor
}

func (r *IdentityResolver) SourceActor(ctx context.Context) contextinfo.Actor {
	actor, hasActor := contextinfo.ActorFromContext(ctx)
	if info, ok := contextinfo.ConversationFromContext(ctx); ok && !hasActor && (info.Identity.PlatformUserID != "" || info.Identity.ActorID != "") {
		policy := r.policy
		if policy == nil {
			policy = security.DefaultPolicy()
		}
		actor = policy.Actor(info.Identity.ActorID, info.Source.Platform, info.Identity.PlatformUserID, info.Identity.DisplayName)
		actor.Nickname, actor.GroupCard = info.Identity.Nickname, info.Identity.GroupCard
		if msg, ok := platform.MessageContextFrom(ctx); ok {
			actor.GroupRole = msg.GroupRole
		}
	}
	return actor
}

func (r *IdentityResolver) IsCLI(ctx context.Context) bool {
	if info, ok := contextinfo.ConversationFromContext(ctx); ok {
		return info.Source.Platform == "cli"
	}
	return r.platformName == "cli"
}

// IdentityResolverDeps declares only the capabilities consumed by IdentityResolver.
type IdentityResolverDeps struct {
	PlatformName string
	ActorID      string
	ScopeID      string
	Policy       *security.Policy
}

// NewIdentityResolver initializes IdentityResolver without installing cross-service callbacks.
func NewIdentityResolver(deps IdentityResolverDeps) (*IdentityResolver, error) {
	if deps.Policy == nil {
		return nil, fmt.Errorf("IdentityResolver policy is required")
	}
	return &IdentityResolver{
		platformName: deps.PlatformName,
		actorID:      deps.ActorID,
		scopeID:      deps.ScopeID,
		policy:       deps.Policy,
	}, nil
}

func (r *IdentityResolver) Policy() *security.Policy { return r.policy }
func (r *IdentityResolver) PlatformName() string     { return r.platformName }
