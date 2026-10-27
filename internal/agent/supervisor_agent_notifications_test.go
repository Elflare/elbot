package agent

import (
	"context"
	"elbot/internal/agent/dialogue"
	"elbot/internal/contextinfo"
	"elbot/internal/notification/rules"
	"elbot/internal/platform/dispatch"
	"testing"
)

func TestNotificationFactsUseGlobalSignalAndVisionDedup(t *testing.T) {
	one, two := &assemblyPlatform{}, &assemblyPlatform{}
	router := dispatch.New(dispatch.Options{Primary: two})
	connectTestNotifications(t, router)
	b, facts := &signalBindings{}, observerSignals()
	if err := b.connectAgentNotifications(facts); err != nil {
		t.Fatal(err)
	}
	defer b.Close(context.Background())
	ctx := context.WithValue(contextinfo.WithConversation(context.Background(), contextinfo.Conversation{Source: contextinfo.Source{Platform: "cli", ScopeID: "original"}}), notificationSenderKey{}, one)
	conversation, _ := contextinfo.ConversationFromContext(ctx)
	for range 2 {
		if _, err := facts.VisionFallbackUsed.Emit(ctx, dialogue.VisionFallbackUsedEvent{SessionID: "one", Conversation: &conversation, Visible: true}); err != nil {
			t.Fatal(err)
		}
	}
	if one.text() != rules.VisionFallback || two.text() != "" {
		t.Fatal("vision dedup or origin lost", one.text(), two.text())
	}
}
