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
