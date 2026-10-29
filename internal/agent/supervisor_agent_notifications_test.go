package agent

import (
	"context"
	"elbot/internal/agent/dialogue"
	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/notification/rules"
	"elbot/internal/signal"
	"log/slog"
	"testing"
)

func TestNotificationFactsUseGlobalSignalAndVisionDedup(t *testing.T) {
	var notices []events.Notification
	connection, err := events.NotificationRequested.Connect(func(_ context.Context, n events.Notification) (events.NotificationReceipt, error) {
		notices = append(notices, n)
		return events.NotificationReceipt{}, nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close(context.Background()) })
	b, facts := &signalBindings{}, observerSignals()
	if err := b.connectAgentNotifications(facts); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = b.Close(context.Background())
		_ = facts.ModelCallCompleted.Close(context.Background())
		_ = facts.StatusChanged.Close(context.Background())
		_ = facts.VisionFallbackUsed.Close(context.Background())
	})
	conversation := contextinfo.Conversation{Source: contextinfo.Source{Platform: "cli", ScopeID: "original"}}
	// The event origin deliberately differs from the ambient context.
	ctx := contextinfo.WithConversation(t.Context(), contextinfo.Conversation{Source: contextinfo.Source{Platform: "other", ScopeID: "wrong"}})
	for range 2 {
		if _, err := facts.VisionFallbackUsed.Emit(ctx, dialogue.VisionFallbackUsedEvent{SessionID: "one", Conversation: &conversation, Visible: true}); err != nil {
			t.Fatal(err)
		}
	}
	if len(notices) != 1 {
		t.Fatalf("notices=%+v", notices)
	}
	n := notices[0]
	if n.Origin == nil || *n.Origin != conversation || n.Target != (events.NotificationTarget{}) || n.Text != rules.VisionFallback || n.Level != slog.LevelWarn {
		t.Fatalf("notice=%+v", n)
	}
}
