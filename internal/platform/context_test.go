package platform

import (
	"context"
	"testing"

	"elbot/internal/contextinfo"
)

func TestMessageContextInstallsPublicSnapshot(t *testing.T) {
	msg := MessageContext{Conversation: contextinfo.Conversation{Source: contextinfo.Source{Platform: "qqonebot", ScopeID: "group:7", ConversationID: "7", ConversationKind: contextinfo.ConversationGroup}, Identity: contextinfo.Identity{PlatformUserID: "10"}, PlatformMessageID: "original"}}
	ctx := WithMessageContext(context.Background(), msg)
	want := msg.Conversation
	msg.Conversation.Identity.PlatformUserID = "other"
	got, ok := contextinfo.ConversationFromContext(ctx)
	if !ok || got != want {
		t.Fatalf("snapshot=%+v", got)
	}
	original, _ := MessageContextFrom(ctx)
	if original.PlatformMessageID != "original" {
		t.Fatal("reply context lost")
	}
	updated := WithMessageContext(ctx, msg)
	got, _ = contextinfo.ConversationFromContext(updated)
	if got != msg.Conversation {
		t.Fatal("public snapshot stale after platform context update")
	}
}

func TestPlatformProjectionUsesOnlyCanonicalConversation(t *testing.T) {
	ctx := WithMessageContext(context.Background(), MessageContext{
		Conversation:          contextinfo.Conversation{PlatformMessageID: "old"},
		BufferAssistantOutput: true,
	})
	ctx = contextinfo.WithConversation(ctx, contextinfo.Conversation{PlatformMessageID: "new"})
	msg, ok := MessageContextFrom(ctx)
	if !ok || msg.PlatformMessageID != "new" || !msg.BufferAssistantOutput {
		t.Fatalf("platform projection = %+v, %v", msg, ok)
	}
	msg, _ = MessageContextFrom(contextinfo.WithoutConversation(ctx))
	if msg.PlatformMessageID != "" {
		t.Fatal("platform retained a second conversation snapshot")
	}
}
