package module

import (
	"context"
	agentevents "elbot/internal/agent/events"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery/dispatch"
	"elbot/internal/hook"
	"elbot/internal/notification/rules"
	"elbot/internal/platform"
	"errors"
	"strings"
	"testing"
	"time"
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
	ctx := platform.WithMessageContext(context.Background(), platform.MessageContext{Conversation: contextinfo.Conversation{Source: contextinfo.Source{Platform: "cli", ScopeID: "original"}}, Sender: one})
	meta := agentevents.EventMeta{SessionID: "one"}
	for range 2 {
		if _, err := facts.VisionFallbackUsed.Emit(ctx, agentevents.VisionFallbackUsedEvent{EventMeta: meta, Visible: true}); err != nil {
			t.Fatal(err)
		}
	}
	if one.text() != rules.VisionFallback || two.text() != "" {
		t.Fatal("vision dedup or origin lost", one.text(), two.text())
	}
	if _, err := facts.HookFailed.Emit(ctx, agentevents.HookFailedEvent{EventMeta: meta, Point: hook.PointLLMResponseReceived, Err: errors.New("failed fact"), Notice: true}); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for !strings.Contains(one.text(), "failed fact") {
		select {
		case <-deadline:
			t.Fatal("failure notification not delivered")
		case <-time.After(time.Millisecond):
		}
	}
	if two.text() != "" {
		t.Fatal("notification moved to another sender")
	}
}
