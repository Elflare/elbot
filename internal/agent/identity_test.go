package agent

import (
	"context"
	"testing"

	"elbot/internal/chatinfo"
	"elbot/internal/hook"
	"elbot/internal/security"
)

func TestPublicChatInfoConsumersAndSecurityPrecedence(t *testing.T) {
	a := &Agent{platform: &fakePlatform{}, actorID: "local", scopeID: "local"}
	ctx := chatinfo.WithInfo(context.Background(), chatinfo.Info{
		Source:   chatinfo.Source{Platform: "qqonebot", ScopeID: "group:9", ConversationKind: chatinfo.ConversationGroup, ConversationID: "9"},
		Identity: chatinfo.Identity{ActorID: "cli:local", PlatformUserID: "qqonebot:123", Nickname: "昵称", GroupCard: "名片", DisplayName: "显示名"},
	})
	actor := a.actor(ctx)
	if actor.ID != "qqonebot:123" || actor.PlatformUserID != "123" || actor.Role != security.RoleUser {
		t.Fatalf("public identity must use canonical user and security policy: %+v", actor)
	}
	scope := a.scope(ctx)
	if scope.Platform != "qqonebot" || scope.PlatformScopeID != "group:9" || scope.ActorID != actor.ID || scope.IsCLI {
		t.Fatalf("scope = %+v", scope)
	}
	parts, err := (conversationMetaSystemPromptSource{}).Parts(ctx, SystemPromptRequest{Scope: scope})
	if err != nil || len(parts) != 1 || parts[0].Content != `meta: platform=qqonebot, conversation=group(id:9), display_name="名片"(id:123).` {
		t.Fatalf("prompt meta = %+v, %v", parts, err)
	}
	event := a.fillHookContext(ctx, hook.Event{})
	if event.Platform.Name != "qqonebot" || event.Platform.ScopeID != "group:9" {
		t.Fatalf("hook source = %+v", event.Platform)
	}
	trusted := security.Actor{ID: "trusted", PlatformUserID: "456", Role: security.RoleSuperadmin}
	if got := a.actor(security.WithActor(ctx, trusted)); got != trusted {
		t.Fatalf("security context must take precedence: %+v", got)
	}
}
