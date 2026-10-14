package hook

import (
	"context"
	"elbot/internal/events"
	"testing"
)

func TestManagerReplaceSwapsHandlersAndKeepsRuntimeIntegrations(t *testing.T) {
	active := newTestManager()

	candidate := newTestManager()

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
	candidate.Register(Registration{
		Point: PointAgentInputPrepared,
		Name:  "replacement",
		Match: Always(),
		Handler: HandlerFunc(func(_ context.Context, event Event) (Event, events.LogResult, error) {
			runs++
			return event, events.LogResult{ResultStatus: events.ResultSucceeded, Content: "test source completed"}, nil
		}),
	})

	active.Replace(candidate)
	if _, _, err := active.Run(context.Background(), Event{Point: PointAgentInputPrepared}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if runs != 1 || observed != 1 {
		t.Fatalf("runs=%d observed=%d, want 1 and 1", runs, observed)
	}

	woken = false
	if _, _, err := active.Run(context.Background(), Event{Point: PointAgentInputPrepared}); err != nil {
		t.Fatalf("Run while sleeping: %v", err)
	}
	if runs != 1 {
		t.Fatalf("runs=%d after wakeup rejection, want 1", runs)
	}
}

func TestManagerReplaceKeepsInFlightSnapshot(t *testing.T) {
	active := newTestManager()

	entered := make(chan struct{})
	release := make(chan struct{})
	runs := make(chan string, 3)
	active.Register(Registration{
		Point: PointAgentInputPrepared,
		Name:  "old.blocking",
		Match: Always(),
		Handler: HandlerFunc(func(_ context.Context, event Event) (Event, events.LogResult, error) {
			close(entered)
			<-release
			runs <- "old.blocking"
			return event, events.LogResult{ResultStatus: events.ResultSucceeded, Content: "test source completed"}, nil
		}),
	})
	active.Register(Registration{
		Point: PointAgentInputPrepared,
		Name:  "old.second",
		Match: Always(),
		Handler: HandlerFunc(func(_ context.Context, event Event) (Event, events.LogResult, error) {
			runs <- "old.second"
			return event, events.LogResult{ResultStatus: events.ResultSucceeded, Content: "test source completed"}, nil
		}),
	})

	done := make(chan error, 1)
	go func() {
		_, _, err := active.Run(context.Background(), Event{Point: PointAgentInputPrepared})
		done <- err
	}()
	<-entered

	candidate := newTestManager()

	candidate.Register(Registration{
		Point: PointAgentInputPrepared,
		Name:  "new",
		Match: Always(),
		Handler: HandlerFunc(func(_ context.Context, event Event) (Event, events.LogResult, error) {
			runs <- "new"
			return event, events.LogResult{ResultStatus: events.ResultSucceeded, Content: "test source completed"}, nil
		}),
	})
	active.Replace(candidate)
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("in-flight Run: %v", err)
	}
	if first, second := <-runs, <-runs; first != "old.blocking" || second != "old.second" {
		t.Fatalf("in-flight handlers = %q, %q", first, second)
	}

	if _, _, err := active.Run(context.Background(), Event{Point: PointAgentInputPrepared}); err != nil {
		t.Fatalf("new Run: %v", err)
	}
	if got := <-runs; got != "new" {
		t.Fatalf("new handler = %q, want new", got)
	}
}

func TestManagerReplacePluginLeavesOtherRegistrationsUntouched(t *testing.T) {
	active := newTestManager()

	candidate := newTestManager()

	runs := []string{}
	register := func(manager *DefaultManager, pluginID, name string) {
		t.Helper()
		manager.Register(Registration{
			Point:    PointAgentInputPrepared,
			PluginID: pluginID,
			Name:     name,
			Match:    Always(),
			Handler: HandlerFunc(func(_ context.Context, event Event) (Event, events.LogResult, error) {
				runs = append(runs, name)
				return event, events.LogResult{ResultStatus: events.ResultSucceeded, Content: "test source completed"}, nil
			}),
		})
	}
	register(active, "demo", "old")
	register(active, "other", "other")
	register(candidate, "demo", "new")
	register(candidate, "unrelated", "unrelated")

	active.ReplacePlugin("demo", candidate)
	if _, _, err := active.Run(context.Background(), Event{Point: PointAgentInputPrepared}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(runs) != 2 || runs[0] != "new" || runs[1] != "other" {
		t.Fatalf("runs = %#v, want new and other", runs)
	}
}
