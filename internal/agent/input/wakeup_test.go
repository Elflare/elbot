package input

import (
	"testing"

	"context"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/storage"
)

type assistantMapping struct{}

func (assistantMapping) FindByPlatformMessage(context.Context, string, contextinfo.ChatType, string, string) (*storage.Message, error) {
	return &storage.Message{Role: storage.RoleAssistant}, nil
}

func TestGroupReplyWakeupUsesKnownSender(t *testing.T) {
	h := NewMessageHandler(MessageHandlerDeps{Commands: &entryProbe{}, Messages: assistantMapping{}})
	msg := agentshare.MessageContext{}
	msg.Bot.UserID = "bot"
	for _, tc := range []struct {
		platform, sender string
		want             bool
	}{
		{config.PlatformQQOfficial, "bot", true},
		{config.PlatformQQOfficial, "member", false},
		{config.PlatformQQOfficial, "", false},
		{config.PlatformQQOneBot, "member", true},
		{config.PlatformTelegram, "member", true},
	} {
		ctx := contextinfo.WithConversation(t.Context(), contextinfo.Conversation{
			Source: contextinfo.Source{Platform: tc.platform, ChatType: contextinfo.ChatGroup}, ReplyToMessageID: "original", ReplyToSenderID: tc.sender,
		})
		ctx = agentshare.WithMessageContext(ctx, msg)
		if got := h.messageWakeup(ctx, "在？"); got != tc.want {
			t.Errorf("%s reply to %q: wakeup=%v, want %v", tc.platform, tc.sender, got, tc.want)
		}
	}
}

func TestWakeupRequiresTriggerOnlyForGroupConversations(t *testing.T) {
	h := NewMessageHandler(MessageHandlerDeps{Commands: &entryProbe{}})
	for _, kind := range []contextinfo.ChatType{contextinfo.ChatPrivate, contextinfo.ChatPrivate, contextinfo.ChatGroup, contextinfo.ChatChannel} {
		t.Run(string(kind), func(t *testing.T) {
			ctx := contextinfo.WithConversation(t.Context(), contextinfo.Conversation{Source: contextinfo.Source{ChatType: kind}})
			ctx = agentshare.WithMessageContext(ctx, agentshare.MessageContext{TriggerKeywords: []string{"hello"}})
			want := kind == contextinfo.ChatPrivate
			if got := h.messageWakeup(ctx, "ordinary message"); got != want {
				t.Fatalf("ordinary input wakeup=%v want=%v", got, want)
			}
			if !h.messageWakeup(ctx, "hello message") {
				t.Fatal("triggered input did not wake")
			}
		})
	}
}
