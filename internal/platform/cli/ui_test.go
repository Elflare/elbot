package cli

import (
	"context"
	"errors"
	"testing"
	"time"

	agentevents "elbot/internal/agent/events"
	"elbot/internal/contextinfo"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/session"
	"elbot/internal/signal"
)

func TestUIConnectFailureReleasesPartialSubscription(t *testing.T) {
	status := signal.New[agentevents.StatusChangedEvent, signal.Empty]("test.status", signal.Options[agentevents.StatusChangedEvent]{Single: true})
	defer status.Close(context.Background())
	binding := signal.New[session.BindingChangedEvent, signal.Empty]("test.binding")
	_ = binding.Close(context.Background())
	if u, err := ConnectUI(status, binding); !errors.Is(err, signal.ErrClosed) || u != nil {
		t.Fatal(u, err)
	}
	// A failed assembly must not retain the single available registration.
	c, err := status.Observe(func(context.Context, agentevents.StatusChangedEvent) error { return nil }, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close(context.Background())
}

func TestUICloseCancelsDisplayAndWaitsForActualExit(t *testing.T) {
	u, status := testUI(t)
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(release)
	u.display.send = func(ctx context.Context, _ messageData, _ runtimestatus.Snapshot) error {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return ctx.Err()
	}
	ctx := contextinfo.WithConversation(context.Background(), contextinfo.Conversation{PlatformData: messageData{adapter: New()}})
	if _, err := status.Emit(ctx, agentevents.StatusChangedEvent{Snapshot: runtimestatus.Snapshot{SessionID: "s", Phase: runtimestatus.PhaseLLM}, Version: 1}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("display never started")
	}
	u.BeginClose()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("display cancellation not propagated")
	}
	deadline, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := u.Close(deadline); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if u.display.stopped() {
		t.Fatal("reported exit while callback still active")
	}
}
