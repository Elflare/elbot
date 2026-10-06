package builtin

import (
	"context"
	"testing"

	"elbot/internal/contextinfo"
)

func TestChatHistoryUsesPublicSourceAndSender(t *testing.T) {
	ctx := contextinfo.WithConversation(context.Background(), contextinfo.Conversation{
		Source:   contextinfo.Source{Platform: "qqofficial", ScopeID: "group:openid"},
		Identity: contextinfo.Identity{ActorID: "qqofficial:user", PlatformUserID: "user"},
	})
	chat, err := currentChatHistoryContext(ctx)
	if err != nil || chat.Platform != "qqofficial" || chat.ScopeID != "group:openid" {
		t.Fatalf("chat = %+v, err = %v", chat, err)
	}
	id, name, errText := chatHistoryUserFilter("我", chat)
	if id != "user" || name != "" || errText != "" {
		t.Fatalf("self filter = %q, %q, %q", id, name, errText)
	}
	if _, err := currentChatHistoryContext(context.Background()); err == nil {
		t.Fatal("missing source must not invent a history scope")
	}
}
