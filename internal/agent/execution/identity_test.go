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
		Source:   contextinfo.Source{Platform: "qqonebot", ChatType: contextinfo.ChatGroup, ChatID: "9"},
		Identity: contextinfo.Identity{ID: "qqonebot:123", Nickname: "昵称", GroupCard: "名片", Name: "显示名"},
	})
	ctx = agentshare.WithMessageContext(ctx, agentshare.MessageContext{GroupRole: contextinfo.GroupRoleAdmin})
	actor := identity.Actor(ctx)
	if actor.ActorIdentity != (contextinfo.ActorIdentity{Platform: "qqonebot", ID: "qqonebot:123"}) || actor.ID != "qqonebot:123" || actor.Role != contextinfo.RoleUser {
		t.Fatalf("public identity must preserve the native user ID and use security policy: %+v", actor)
	}
	if actor.GroupRole != contextinfo.GroupRoleAdmin || actor.Nickname != "昵称" || actor.GroupCard != "名片" || actor.Name != "显示名" {
		t.Fatalf("platform identity facts changed: %+v", actor)
	}
	scope := identity.Scope(ctx)
	if scope.Platform != "qqonebot" || (scope.ChatType != "group" || scope.ChatID != "9") || scope.Owner != actor.ActorIdentity || scope.Owner.ID != actor.ID || scope.IsCLI {
		t.Fatalf("scope = %+v", scope)
	}
	trusted := contextinfo.Actor{ActorIdentity: contextinfo.ActorIdentity{Platform: "cli", ID: "456"}, Role: contextinfo.RoleSuperadmin}
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
		Source:   contextinfo.Source{Platform: "cli", ChatID: "laptop", ChatType: "private"},
		Identity: contextinfo.Identity{ID: "laptop"},
	})
	if actor, scope := identity.Actor(ctx), identity.Scope(ctx); actor.ID != "laptop" || actor.Role != contextinfo.RoleUser || scope.ChatID != "laptop" || scope.IsCLI {
		t.Fatalf("remote user acquired local privileges: actor=%+v scope=%+v", actor, scope)
	}
}

func TestIdentityKeepsNamesOutsideOwnership(t *testing.T) {
	resolver := agentshare.NewIdentityResolver(agentshare.IdentityResolverDeps{CLIPlatformName: "cli", Permission: security.DefaultPermission()})
	info := contextinfo.Conversation{Source: contextinfo.Source{Platform: "telegram", ChatType: contextinfo.ChatGroup, ChatID: "group:42", Name: "旧群名"}, Identity: contextinfo.Identity{ID: "user:9", Name: "旧昵称"}}
	before := resolver.Scope(contextinfo.WithConversation(t.Context(), info))
	info.Source.Name, info.Identity.Name = "新群名", "新昵称"
	ctx := contextinfo.WithConversation(t.Context(), info)
	after := resolver.Scope(ctx)
	if before != after || resolver.Actor(ctx).Name != "新昵称" {
		t.Fatalf("name changed ownership: %+v %+v", before, after)
	}
}
