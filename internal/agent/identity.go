package agent

import (
	"context"
	"strings"

	"elbot/internal/platform"
	"elbot/internal/security"
	"elbot/internal/session"
)

func (a *Agent) scope(ctx context.Context) session.Scope {
	actor := a.actor(ctx)
	platformName := a.platform.Name()
	scopeID := a.scopeID
	if msg, ok := platform.MessageContextFrom(ctx); ok {
		if msg.Platform != "" {
			platformName = msg.Platform
		}
		if msg.ScopeID != "" {
			scopeID = msg.ScopeID
		}
	}
	return session.Scope{
		ActorID:         actor.ID,
		Platform:        platformName,
		PlatformScopeID: scopeID,
		IsCLI:           platformName == "cli" && actor.Role == security.RoleSuperadmin,
	}
}

func (a *Agent) conversationMeta(ctx context.Context, scope session.Scope) ConversationMeta {
	meta := ConversationMeta{Platform: strings.TrimSpace(scope.Platform)}
	msg, ok := platform.MessageContextFrom(ctx)
	if !ok {
		return meta
	}

	switch msg.ConversationKind {
	case platform.ConversationGroup:
		meta.Kind = "group"
	case platform.ConversationPrivate:
		meta.Kind = "private"
	case platform.ConversationChannel:
		meta.Kind = "channel"
	}
	if meta.Kind == "" {
		meta.Kind = conversationKindFromScope(scope.PlatformScopeID)
	}

	actor := a.actor(ctx)
	switch meta.Kind {
	case "group":
		meta.ID = conversationID(scope.PlatformScopeID, "group:", "supergroup:")
		meta.DisplayName = firstNonEmpty(actor.GroupCard, actor.Nickname)
	case "private":
		meta.ID = strings.TrimSpace(actor.PlatformUserID)
		meta.DisplayName = actor.Nickname
	case "channel":
		meta.ID = conversationID(scope.PlatformScopeID, "channel:")
		meta.DisplayName = actor.Nickname
	}
	return meta
}

func conversationKindFromScope(scopeID string) string {
	switch {
	case strings.HasPrefix(scopeID, "group:"), strings.HasPrefix(scopeID, "supergroup:"):
		return "group"
	case strings.HasPrefix(scopeID, "private:"), strings.HasPrefix(scopeID, "c2c:"):
		return "private"
	case strings.HasPrefix(scopeID, "channel:"):
		return "channel"
	default:
		return ""
	}
}

func conversationID(scopeID string, prefixes ...string) string {
	for _, prefix := range prefixes {
		if strings.HasPrefix(scopeID, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(scopeID, prefix))
		}
	}
	return ""
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
	displayName := ""
	actorID := ""
	groupRole := security.GroupRoleUnknown
	nickname := ""
	groupCard := ""
	if msg, ok := platform.MessageContextFrom(ctx); ok {
		if msg.Platform != "" {
			platformName = msg.Platform
		}
		if msg.PlatformUserID != "" {
			platformUserID = msg.PlatformUserID
		}
		actorID = msg.ActorID
		displayName = msg.DisplayName
		nickname = msg.Nickname
		groupCard = msg.GroupCard
		groupRole = security.ParseGroupRole(string(msg.GroupRole))
	}
	if prefix := platformName + ":"; strings.HasPrefix(platformUserID, prefix) {
		platformUserID = strings.TrimPrefix(platformUserID, prefix)
	}
	policy := a.securityPolicy
	if policy == nil {
		policy = security.DefaultPolicy()
	}
	actor := policy.Actor(actorID, platformName, platformUserID, displayName)
	actor.Nickname = nickname
	actor.GroupCard = groupCard
	actor.GroupRole = groupRole
	return actor
}
