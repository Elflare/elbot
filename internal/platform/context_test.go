package platform

import (
	"context"
	"testing"

	"elbot/internal/chatinfo"
)

func TestMessageContextInstallsPublicSnapshot(t *testing.T) {
	msg := MessageContext{Info: chatinfo.Info{Source: chatinfo.Source{Platform: "qqonebot", ScopeID: "group:7", ConversationID: "7", ConversationKind: chatinfo.ConversationGroup}, Identity: chatinfo.Identity{PlatformUserID: "10"}, PlatformMessageID: "original"}}
	ctx := WithMessageContext(context.Background(), msg)
	want := msg.Info
	msg.Info.Identity.PlatformUserID = "other"
	got, ok := chatinfo.FromContext(ctx)
	if !ok || got != want {
		t.Fatalf("snapshot=%+v", got)
	}
	original, _ := MessageContextFrom(ctx)
	if original.PlatformMessageID != "original" {
		t.Fatal("reply context lost")
	}
	updated := WithMessageContext(ctx, msg)
	got, _ = chatinfo.FromContext(updated)
	if got != msg.Info {
		t.Fatal("public snapshot stale after platform context update")
	}
}
