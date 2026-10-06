package app

import (
	"context"
	"errors"
	"testing"
	"time"

	agentevents "elbot/internal/agent/events"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/delivery/dispatch"
	"elbot/internal/hook"
	"elbot/internal/notification"
	"elbot/internal/notification/rules"
	"elbot/internal/platform"
	"elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/storage/sqlite"
)

type noticeAssistant struct{ router *dispatch.Router }

func (s noticeAssistant) SendAssistant(ctx context.Context, text string) (delivery.Receipt, error) {
	return s.router.SendChat(ctx, []delivery.Output{delivery.Text(text)})
}

func holdObserverQueue(t *testing.T, queue *signal.Queue) func() {
	t.Helper()
	started, release := make(chan struct{}), make(chan struct{})
	if err := queue.Submit(context.Background(), signal.Task{Run: func(ctx context.Context) error {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	<-started
	return func() { close(release) }
}
func flushObserverQueue(t *testing.T, queue *signal.Queue) {
	t.Helper()
	done := make(chan struct{})
	if err := queue.Submit(context.Background(), signal.Task{Run: func(context.Context) error { close(done); return nil }}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("observer did not finish")
	}
}

func TestNoticeLifetimesOriginalSourceAndVisionDedup(t *testing.T) {
	one, two := &assemblyPlatform{}, &assemblyPlatform{}
	router := dispatch.New(dispatch.Options{Primary: two})
	b, events := &signalBindings{}, observerSignals()
	if err := b.connectAgentNotifications(events, notification.New(router, nil, false), noticeAssistant{router}, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	releaseProgress, releaseFailure := holdObserverQueue(t, b.queues[0]), holdObserverQueue(t, b.queues[1])
	ctx, cancel := context.WithCancel(platform.WithMessageContext(context.Background(), platform.MessageContext{Conversation: contextinfo.Conversation{Source: contextinfo.Source{Platform: "cli", ScopeID: "original"}}, Sender: one}))
	meta := agentevents.EventMeta{At: time.Now(), SessionID: "s"}
	if err := events.VisionFallbackUsed.Emit(ctx, agentevents.VisionFallbackUsedEvent{EventMeta: meta, Visible: true}); err != nil {
		t.Fatal(err)
	}
	if err := events.HookFailed.Emit(ctx, agentevents.HookFailedEvent{EventMeta: meta, Point: hook.PointLLMResponseReceived, Err: errors.New("failed fact"), Notice: true}); err != nil {
		t.Fatal(err)
	}
	cancel()
	releaseProgress()
	releaseFailure()
	flushObserverQueue(t, b.queues[0])
	flushObserverQueue(t, b.queues[1])
	if got, want := one.text(), "Hook 执行失败（"+string(hook.PointLLMResponseReceived)+"）：\nfailed fact"; got != want {
		t.Fatalf("request completion canceled fact or leaked progress: %q", got)
	}
	if two.text() != "" {
		t.Fatal("old notification moved to current destination")
	}
	active := platform.WithMessageContext(context.Background(), platform.MessageContext{Sender: one})
	for range 2 {
		if err := events.VisionFallbackUsed.Emit(active, agentevents.VisionFallbackUsedEvent{EventMeta: meta, Visible: true}); err != nil {
			t.Fatal(err)
		}
	}
	flushObserverQueue(t, b.queues[0])
	if got := one.text(); got != "Hook 执行失败（"+string(hook.PointLLMResponseReceived)+"）：\nfailed fact\n"+rules.VisionFallback {
		t.Fatalf("canceled hint consumed dedup or duplicate hint sent: %q", got)
	}
}

func TestQueuedFailureChecksOriginalBinding(t *testing.T) {
	store, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := session.NewService(store)
	scope := session.Scope{ActorID: "cli:local", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	if _, err := sessions.Create(context.Background(), scope, session.CreateRequest{}); err != nil {
		t.Fatal(err)
	}
	row, binding, err := sessions.CurrentBound(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	p := &assemblyPlatform{}
	router := dispatch.New(dispatch.Options{Primary: p})
	b, events := &signalBindings{}, observerSignals()
	if err := b.connectAgentNotifications(events, notification.New(router, nil, false), noticeAssistant{router}, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	release := holdObserverQueue(t, b.queues[1])
	ctx := session.WithBinding(context.Background(), binding)
	if err := events.HookFailed.Emit(ctx, agentevents.HookFailedEvent{EventMeta: agentevents.EventMeta{SessionID: row.ID}, Point: hook.PointLLMResponseReceived, Err: errors.New("old"), Notice: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Create(context.Background(), scope, session.CreateRequest{Title: "new"}); err != nil {
		t.Fatal(err)
	}
	release()
	flushObserverQueue(t, b.queues[1])
	if p.text() != "" {
		t.Fatalf("expired binding delivered: %q", p.text())
	}
}
