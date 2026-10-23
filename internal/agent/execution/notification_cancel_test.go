package execution

import (
	"context"
	"testing"
	"time"

	agentoutput "elbot/internal/agent/output"
	"elbot/internal/config"
	"elbot/internal/events"
	"elbot/internal/notification"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/signal"
)

func TestCanceledParentPublishesTerminalStatus(t *testing.T) {
	p := &fakePlatform{}
	started := make(chan struct{})
	a := newTestAgent(t, p, &fakeLLM{chatBlocks: []fakeLLMBlock{{started: started, release: make(chan struct{})}}}, "test-model", config.ProviderConfig{}, newTestStore(t))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.HandleMessage(ctx, "hello") }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("model not started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("chat did not stop")
	}
	_, shown := p.statusSnapshot()
	t.Logf("displayed phase=%s running=%t", shown.Phase, shown.Running())
	if shown.Phase != runtimestatus.PhaseDone && shown.Phase != runtimestatus.PhaseError {
		t.Errorf("completed/canceled conversation still displayed as %s", shown.Phase)
	}
}

func TestQueuedPreviewRespectsCancellation(t *testing.T) {
	m, err := notification.New("cli")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(context.Background())
	s, err := m.AddPlatform("cli")
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	seen := make(chan string, 8)
	_, err = s.Connect(func(_ context.Context, n events.Notification) (events.NotificationReceipt, error) {
		if n.Text == "block" {
			close(entered)
			<-release
		} else {
			seen <- n.Text
		}
		return events.NotificationReceipt{}, nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = events.NotificationRequested.EmitAsync(context.Background(), events.Notification{Text: "block"})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	out := agentoutput.NewOutputSender(agentoutput.OutputSenderDeps{Hooks: constructorHooks{}})
	out.Preview(ctx, "stale preview")
	cancel()
	last, err := events.NotificationRequested.EmitAsync(context.Background(), events.Notification{Text: "marker"})
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	if _, err = last.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(seen)
	for text := range seen {
		if text == "[tool] stale preview" {
			t.Error("preview was sent after its request was canceled")
		}
	}
}
