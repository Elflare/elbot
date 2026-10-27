package platform

import (
	"context"
	"encoding/json"
	"testing"

	"elbot/internal/contextinfo"
)

func TestMessageAndConversationFactsAreIndependent(t *testing.T) {
	origin := contextinfo.Conversation{Source: contextinfo.Source{Platform: "telegram", ScopeID: "private:7"}, PlatformMessageID: "source"}
	parent := contextinfo.WithConversation(context.Background(), origin)
	msg := MessageContext{RawText: "message", Reply: ReplyContext{MessageID: "reply"}}
	ctx := WithMessageContext(parent, msg)
	if got, ok := contextinfo.ConversationFromContext(ctx); !ok || got != origin {
		t.Fatalf("installing message changed conversation: %+v, %v", got, ok)
	}
	updated := origin
	updated.PlatformMessageID = "updated"
	ctx = contextinfo.WithConversation(ctx, updated)
	for _, candidate := range []context.Context{ctx, contextinfo.WithoutConversation(ctx)} {
		got, ok := MessageContextFrom(candidate)
		if !ok || got.RawText != msg.RawText || got.Reply.MessageID != msg.Reply.MessageID {
			t.Fatalf("conversation changed message facts: %+v, %v", got, ok)
		}
	}
	if got, ok := contextinfo.ConversationFromContext(WithoutMessageContext(ctx)); !ok || got != updated {
		t.Fatalf("removing message changed conversation: %+v, %v", got, ok)
	}
}

func TestMessageContextSnapshotsAreIndependent(t *testing.T) {
	type factKey struct{}
	parent := context.WithValue(context.Background(), factKey{}, "conversation")
	input := MessageContext{
		RawText: "original", PlatformMessage: json.RawMessage(`{"id":1}`),
		Reply: ReplyContext{MessageID: "reply", SenderID: "actor", SenderName: "name", Text: "quoted",
			Segments: []MessageSegment{{Type: SegmentImage, MediaID: "media", Name: "image"}}},
	}
	ctx := WithMessageContext(parent, input)
	input.PlatformMessage[0] = '!'
	input.Reply.Segments[0].Name = "changed input"
	first, ok := MessageContextFrom(ctx)
	if !ok || first.RawText != "original" || string(first.PlatformMessage) != `{"id":1}` || first.Reply.Segments[0].Name != "image" {
		t.Fatalf("stored snapshot changed: %+v", first)
	}
	first.PlatformMessage[0] = '?'
	first.Reply.Segments[0].Name = "changed reader"
	second, ok := MessageContextFrom(ctx)
	if !ok || string(second.PlatformMessage) != `{"id":1}` || second.Reply.Segments[0].Name != "image" {
		t.Fatalf("readers share mutable facts: %+v", second)
	}
	if ctx.Value(factKey{}) != "conversation" {
		t.Fatal("lost unrelated context facts")
	}
	without := WithoutMessageContext(ctx)
	if _, ok := MessageContextFrom(without); ok {
		t.Fatal("message facts still visible")
	}
	if without.Value(factKey{}) != "conversation" {
		t.Fatal("removing message lost other facts")
	}
	if _, ok := MessageContextFrom(ctx); !ok {
		t.Fatal("removing message changed original context")
	}
}

func TestMessageContextAbsentAndEmpty(t *testing.T) {
	if _, ok := MessageContextFrom(context.Background()); ok {
		t.Fatal("unexpected message")
	}
	msg, ok := MessageContextFrom(WithMessageContext(context.Background(), MessageContext{}))
	if !ok || msg.PlatformMessage != nil || msg.Reply.Segments != nil {
		t.Fatalf("empty snapshot: %+v, %v", msg, ok)
	}
}
