package execution

import (
	"context"
	"elbot/internal/security"
	"testing"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/session"
)

func TestPublicChatInfoConsumersAndSecurityPrecedence(t *testing.T) {
	identity := agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{CLIPlatformName: "cli", Permission: security.DefaultPermission()})
	ctx := contextinfo.WithConversation(context.Background(), contextinfo.Conversation{
		Source:   contextinfo.Source{Platform: "qqonebot", ScopeID: "group:9", ConversationKind: contextinfo.ConversationGroup, ConversationID: "9"},
		Identity: contextinfo.Identity{ActorID: "cli:local", PlatformUserID: "qqonebot:123", Nickname: "昵称", GroupCard: "名片", DisplayName: "显示名"},
	})
	ctx = agentshare.WithMessageContext(ctx, agentshare.MessageContext{GroupRole: contextinfo.GroupRoleAdmin})
	actor := identity.Actor(ctx)
	if actor.ID != "qqonebot:qqonebot:123" || actor.PlatformUserID != "qqonebot:123" || actor.Role != contextinfo.RoleUser {
		t.Fatalf("public identity must preserve the native user ID and use security policy: %+v", actor)
	}
	if actor.GroupRole != contextinfo.GroupRoleAdmin || actor.Nickname != "昵称" || actor.GroupCard != "名片" || actor.DisplayName != "显示名" {
		t.Fatalf("platform identity facts changed: %+v", actor)
	}
	scope := identity.Scope(ctx)
	if scope.Platform != "qqonebot" || scope.PlatformScopeID != "group:9" || scope.ActorID != actor.ID || scope.PlatformUserID != actor.PlatformUserID || scope.IsCLI {
		t.Fatalf("scope = %+v", scope)
	}
	trusted := contextinfo.Actor{ID: "trusted", PlatformUserID: "456", Role: contextinfo.RoleSuperadmin}
	if got := identity.Actor(contextinfo.WithActor(ctx, trusted)); got != trusted {
		t.Fatalf("security context must take precedence: %+v", got)
	}
}

func TestIdentityResolverDoesNotInventEntryDefaults(t *testing.T) {
	identity := agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{CLIPlatformName: "cli", Permission: security.DefaultPermission()})
	if actor, scope := identity.Actor(t.Context()), identity.Scope(t.Context()); actor != (contextinfo.Actor{}) || scope != (session.Scope{}) || identity.IsCLI(t.Context()) {
		t.Fatalf("source-free request acquired an identity: actor=%+v scope=%+v", actor, scope)
	}
	ctx := contextinfo.WithConversation(t.Context(), contextinfo.Conversation{
		Source:   contextinfo.Source{Platform: "cli"},
		Identity: contextinfo.Identity{ActorID: "cli:local"},
	})
	if actor, scope := identity.Actor(ctx), identity.Scope(ctx); actor != (contextinfo.Actor{}) || scope != (session.Scope{Platform: "cli"}) {
		t.Fatalf("partial source acquired default user or scope: actor=%+v scope=%+v", actor, scope)
	}
	ctx = contextinfo.WithConversation(t.Context(), contextinfo.Conversation{
		Source:   contextinfo.Source{Platform: "cli", ScopeID: "laptop"},
		Identity: contextinfo.Identity{PlatformUserID: "laptop"},
	})
	if actor, scope := identity.Actor(ctx), identity.Scope(ctx); actor.PlatformUserID != "laptop" || actor.Role != contextinfo.RoleUser || scope.PlatformScopeID != "laptop" || scope.IsCLI {
		t.Fatalf("remote user acquired local privileges: actor=%+v scope=%+v", actor, scope)
	}
}
