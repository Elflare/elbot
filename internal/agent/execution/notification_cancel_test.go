package execution

import (
	"context"
	"testing"

	agenthooks "elbot/internal/agent/hooks"
	agentoutput "elbot/internal/agent/output"

	"elbot/internal/events"
	"elbot/internal/platform/notification"

	"elbot/internal/signal"
)

func TestQueuedPreviewRespectsCancellation(t *testing.T) {
	m, err := notification.New("cli", func(context.Context, events.LogRecord) {})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(context.Background())
	s, err := m.AddPlatform("cli", func(events.NotificationTarget) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	seen := make(chan string, 8)
	_, err = s.Observe(func(_ context.Context, n events.Notification) {
		if n.Text == "block" {
			close(entered)
			<-release
		} else {
			seen <- n.Text
		}

	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	events.NotificationRequested.EmitAsync(context.Background(), events.Notification{Text: "block"})
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	out := agentoutput.NewOutputSender(agentoutput.OutputSenderDeps{Hooks: agenthooks.NewHookBridge(agenthooks.HookBridgeDeps{})})
	out.Preview(ctx, "stale preview")
	cancel()
	close(release)
	if _, err = events.NotificationRequested.Emit(context.Background(), events.Notification{Text: "marker"}); err != nil {
		t.Fatal(err)
	}
	close(seen)
	for text := range seen {
		if text == "[tool] stale preview" {
			t.Error("preview was sent after its request was canceled")
		}
	}
}
