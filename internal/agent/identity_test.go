package agent

import (
	"context"
	"testing"

	"elbot/internal/agent/dialogue"
	"elbot/internal/contextinfo"
	"elbot/internal/hook"
)

func TestPublicChatInfoConsumersAndSecurityPrecedence(t *testing.T) {
	identity := &identityResolver{platformName: "test", actorID: "local", scopeID: "local"}
	bridge := &hookBridge{identity: identity}
	ctx := contextinfo.WithConversation(context.Background(), contextinfo.Conversation{
		Source:   contextinfo.Source{Platform: "qqonebot", ScopeID: "group:9", ConversationKind: contextinfo.ConversationGroup, ConversationID: "9"},
		Identity: contextinfo.Identity{ActorID: "cli:local", PlatformUserID: "qqonebot:123", Nickname: "昵称", GroupCard: "名片", DisplayName: "显示名"},
	})
	actor := identity.Actor(ctx)
	if actor.ID != "qqonebot:123" || actor.PlatformUserID != "123" || actor.Role != contextinfo.RoleUser {
		t.Fatalf("public identity must use canonical user and security policy: %+v", actor)
	}
	scope := identity.Scope(ctx)
	if scope.Platform != "qqonebot" || scope.PlatformScopeID != "group:9" || scope.ActorID != actor.ID || scope.IsCLI {
		t.Fatalf("scope = %+v", scope)
	}
	parts, err := (dialogue.ConversationMetaSystemPromptSource{}).Parts(ctx, dialogue.SystemPromptRequest{Scope: scope})
	if err != nil || len(parts) != 1 || parts[0].Content != `meta: platform=qqonebot, conversation=group(id:9), display_name="名片"(id:123).` {
		t.Fatalf("prompt meta = %+v, %v", parts, err)
	}
	event := bridge.fillContext(ctx, hook.Event{})
	if event.Platform.Name != "qqonebot" || event.Platform.ScopeID != "group:9" {
		t.Fatalf("hook source = %+v", event.Platform)
	}
	trusted := contextinfo.Actor{ID: "trusted", PlatformUserID: "456", Role: contextinfo.RoleSuperadmin}
	if got := identity.Actor(contextinfo.WithActor(ctx, trusted)); got != trusted {
		t.Fatalf("security context must take precedence: %+v", got)
	}
}
