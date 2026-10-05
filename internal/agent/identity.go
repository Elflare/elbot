package agent

import (
	"context"
	"strings"

	"elbot/internal/chatinfo"
	"elbot/internal/platform"
	"elbot/internal/security"
	"elbot/internal/session"
)

func (a *Agent) scope(ctx context.Context) session.Scope {
	actor := a.actor(ctx)
	platformName := a.platform.Name()
	scopeID := a.scopeID
	kind := chatinfo.ConversationUnknown
	if info, ok := chatinfo.FromContext(ctx); ok {
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
		IsCLI:            platformName == "cli" && actor.Role == security.RoleSuperadmin,
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

func (a *Agent) actor(ctx context.Context) security.Actor {
	if actor, ok := security.ActorFromContext(ctx); ok && (actor.ID != "" || actor.Role != "") {
		return actor
	}
	platformName := a.platform.Name()
	platformUserID := a.actorID
	identity := chatinfo.Identity{}
	if info, ok := chatinfo.FromContext(ctx); ok {
		if info.Source.Platform != "" {
			platformName = info.Source.Platform
		}
		if info.Identity.PlatformUserID != "" {
			platformUserID = info.Identity.PlatformUserID
		}
		identity = info.Identity
	}
	groupRole := security.GroupRoleUnknown
	if msg, ok := platform.MessageContextFrom(ctx); ok {
		groupRole = security.ParseGroupRole(string(msg.GroupRole))
	}
	if prefix := platformName + ":"; strings.HasPrefix(platformUserID, prefix) {
		platformUserID = strings.TrimPrefix(platformUserID, prefix)
	}
	policy := a.securityPolicy
	if policy == nil {
		policy = security.DefaultPolicy()
	}
	actor := policy.Actor(identity.ActorID, platformName, platformUserID, identity.DisplayName)
	actor.Nickname = identity.Nickname
	actor.GroupCard = identity.GroupCard
	actor.GroupRole = groupRole
	return actor
}
