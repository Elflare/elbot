package hook

import (
	"context"
	"testing"
)

func TestManagerReplaceSwapsHandlersAndKeepsRuntimeIntegrations(t *testing.T) {
	active := NewManager()
	candidate := NewManager()

	woken := true
	active.SetWakeupFunc(func(context.Context, Event) bool { return woken })
	observed := 0
	active.SetObserver(func(ctx context.Context, _ Event, _ ObserverInfo) (context.Context, func()) {
		observed++
		return ctx, func() {}
	})
	candidate.SetWakeupFunc(func(context.Context, Event) bool { return false })
	candidate.SetObserver(func(ctx context.Context, _ Event, _ ObserverInfo) (context.Context, func()) {
		t.Fatal("candidate observer must not replace active integration")
		return ctx, func() {}
	})

	runs := 0
	if err := candidate.Register(Registration{
		Point: PointAgentInputPrepared,
		Name:  "replacement",
		Match: Always(),
		Handler: HandlerFunc(func(_ context.Context, event Event) (Event, error) {
			runs++
			return event, nil
		}),
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	active.Replace(candidate)
	if _, err := active.Run(context.Background(), Event{Point: PointAgentInputPrepared}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if runs != 1 || observed != 1 {
		t.Fatalf("runs=%d observed=%d, want 1 and 1", runs, observed)
	}

	woken = false
	if _, err := active.Run(context.Background(), Event{Point: PointAgentInputPrepared}); err != nil {
		t.Fatalf("Run while sleeping: %v", err)
	}
	if runs != 1 {
		t.Fatalf("runs=%d after wakeup rejection, want 1", runs)
	}
}

func TestManagerReplaceKeepsInFlightSnapshot(t *testing.T) {
	active := NewManager()
	entered := make(chan struct{})
	release := make(chan struct{})
	runs := make(chan string, 3)
	if err := active.Register(Registration{
		Point: PointAgentInputPrepared,
		Name:  "old.blocking",
		Match: Always(),
		Handler: HandlerFunc(func(_ context.Context, event Event) (Event, error) {
			close(entered)
			<-release
			runs <- "old.blocking"
			return event, nil
		}),
	}); err != nil {
		t.Fatalf("Register old.blocking: %v", err)
	}
	if err := active.Register(Registration{
		Point: PointAgentInputPrepared,
		Name:  "old.second",
		Match: Always(),
		Handler: HandlerFunc(func(_ context.Context, event Event) (Event, error) {
			runs <- "old.second"
			return event, nil
		}),
	}); err != nil {
		t.Fatalf("Register old.second: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := active.Run(context.Background(), Event{Point: PointAgentInputPrepared})
		done <- err
	}()
	<-entered

	candidate := NewManager()
	if err := candidate.Register(Registration{
		Point: PointAgentInputPrepared,
		Name:  "new",
		Match: Always(),
		Handler: HandlerFunc(func(_ context.Context, event Event) (Event, error) {
			runs <- "new"
			return event, nil
		}),
	}); err != nil {
		t.Fatalf("Register new: %v", err)
	}
	active.Replace(candidate)
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("in-flight Run: %v", err)
	}
	if first, second := <-runs, <-runs; first != "old.blocking" || second != "old.second" {
		t.Fatalf("in-flight handlers = %q, %q", first, second)
	}

	if _, err := active.Run(context.Background(), Event{Point: PointAgentInputPrepared}); err != nil {
		t.Fatalf("new Run: %v", err)
	}
	if got := <-runs; got != "new" {
		t.Fatalf("new handler = %q, want new", got)
	}
}
