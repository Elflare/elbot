package input

import (
	"testing"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
)

func TestWakeupRequiresTriggerOnlyForGroupConversations(t *testing.T) {
	h := &MessageHandler{}
	for _, kind := range []contextinfo.ConversationKind{contextinfo.ConversationCLI, contextinfo.ConversationPrivate, contextinfo.ConversationGroup, contextinfo.ConversationChannel} {
		t.Run(string(kind), func(t *testing.T) {
			ctx := contextinfo.WithConversation(t.Context(), contextinfo.Conversation{Source: contextinfo.Source{ConversationKind: kind}})
			ctx = agentshare.WithMessageContext(ctx, agentshare.MessageContext{TriggerKeywords: []string{"hello"}})
			want := kind == contextinfo.ConversationCLI || kind == contextinfo.ConversationPrivate
			if got := h.messageWakeup(ctx, "ordinary message"); got != want {
				t.Fatalf("ordinary input wakeup=%v want=%v", got, want)
			}
			if !h.messageWakeup(ctx, "hello message") {
				t.Fatal("triggered input did not wake")
			}
		})
	}
}
