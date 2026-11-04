package input

import (
	"testing"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
)

func TestWakeupRequiresTriggerOnlyForGroupConversations(t *testing.T) {
	h := &MessageHandler{commands: &entryProbe{}}
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
