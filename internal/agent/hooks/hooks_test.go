package hooks

import (
	"context"
	"errors"
	"testing"
	"time"

	agentevents "elbot/internal/agent/events"
	"elbot/internal/hook"
	"elbot/internal/signal"
)

func TestHookFailureFactOutlivesCanceledPublisher(t *testing.T) {
	failed := agentevents.NewSignals().HookFailed
	want := errors.New("late hook failure")
	seen := make(chan struct{}, 1)
	connection, err := failed.Observe(func(ctx context.Context, event agentevents.HookFailedEvent) error {
		if ctx.Err() != nil || !event.Notice || !errors.Is(event.Err, want) || event.Point != hook.PointAgentOutputPrepared {
			t.Errorf("failure fact lost its uncanceled delivery context or payload: context=%v event=%+v", ctx.Err(), event)
		}
		seen <- struct{}{}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(context.Background())
	bridge := NewHookBridge(HookBridgeDeps{Failed: failed})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	bridge.publishFailure(ctx, hook.Event{Point: hook.PointAgentOutputPrepared}, want, true)
	select {
	case <-seen:
	case <-time.After(time.Second):
		t.Fatal("cancellation prevented the failure fact from reaching its consumer")
	}
}
