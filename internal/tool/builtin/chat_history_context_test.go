package builtin

import (
	"context"
	"testing"

	"elbot/internal/chatinfo"
)

func TestChatHistoryUsesPublicSourceAndSender(t *testing.T) {
	ctx := chatinfo.WithInfo(context.Background(), chatinfo.Info{
		Source:   chatinfo.Source{Platform: "qqofficial", ScopeID: "group:openid"},
		Identity: chatinfo.Identity{ActorID: "qqofficial:user", PlatformUserID: "user"},
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
