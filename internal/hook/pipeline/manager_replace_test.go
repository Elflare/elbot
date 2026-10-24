// 验证 Hook 注册替换与在途执行快照。

package pipeline

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/hook/share"
	"testing"
)

func TestManagerReplaceSwapsHandlersAndKeepsRuntimeIntegrations(t *testing.T) {
	routed, logged, observed, runs := 0, 0, 0, 0
	active := NewManager(func(context.Context, events.LogRecord) { logged++ }, func(_ context.Context, event share.Event, _ func(context.Context, string) (context.Context, func())) (share.Event, bool, events.LogResult, error) {
		routed++
		return event, false, events.LogResult{ResultStatus: events.ResultSkipped}, nil
	})
	candidate := newTestManager()
	candidate.Register(share.Registration{Point: share.PointPlatformMessageReceived, Name: "replacement", Match: share.Always(), Handler: share.HandlerFunc(func(_ context.Context, event share.Event, _ bool) (share.Event, events.LogResult, error) {
		runs++
		return event, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
	})})
	active.Replace(candidate)
	options := CallOptions{Woken: true, Observe: func(ctx context.Context, name string) (context.Context, func()) {
		observed++
		return ctx, func() {}
	}}
	if _, _, err := active.Run(t.Context(), share.Event{Point: share.PointPlatformMessageReceived}, options); err != nil {
		t.Fatal(err)
	}
	options.Woken = false
	if _, _, err := active.Run(t.Context(), share.Event{Point: share.PointPlatformMessageReceived}, options); err != nil {
		t.Fatal(err)
	}
	if routed != 2 || logged != 1 || observed != 1 || runs != 1 {
		t.Fatalf("routed=%d logged=%d observed=%d runs=%d", routed, logged, observed, runs)
	}
}

func TestManagerReplaceKeepsInFlightSnapshot(t *testing.T) {
	active := newTestManager()

	entered := make(chan struct{})
	release := make(chan struct{})
	runs := make(chan string, 3)
	active.Register(share.Registration{
		Point: share.PointAgentInputPrepared,
		Name:  "old.blocking",
		Match: share.Always(),
		Handler: share.HandlerFunc(func(_ context.Context, event share.Event, _ bool) (share.Event, events.LogResult, error) {
			close(entered)
			<-release
			runs <- "old.blocking"
			return event, events.LogResult{ResultStatus: events.ResultSucceeded, Content: "test source completed"}, nil
		}),
	})
	active.Register(share.Registration{
		Point: share.PointAgentInputPrepared,
		Name:  "old.second",
		Match: share.Always(),
		Handler: share.HandlerFunc(func(_ context.Context, event share.Event, _ bool) (share.Event, events.LogResult, error) {
			runs <- "old.second"
			return event, events.LogResult{ResultStatus: events.ResultSucceeded, Content: "test source completed"}, nil
		}),
	})

	done := make(chan error, 1)
	go func() {
		_, _, err := active.Run(context.Background(), share.Event{Point: share.PointAgentInputPrepared}, CallOptions{Woken: true})
		done <- err
	}()
	<-entered

	candidate := newTestManager()

	candidate.Register(share.Registration{
		Point: share.PointAgentInputPrepared,
		Name:  "new",
		Match: share.Always(),
		Handler: share.HandlerFunc(func(_ context.Context, event share.Event, _ bool) (share.Event, events.LogResult, error) {
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

	if _, _, err := active.Run(context.Background(), share.Event{Point: share.PointAgentInputPrepared}, CallOptions{Woken: true}); err != nil {
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
	register := func(manager *Manager, pluginID, name string) {
		t.Helper()
		manager.Register(share.Registration{
			Point:    share.PointAgentInputPrepared,
			PluginID: pluginID,
			Name:     name,
			Match:    share.Always(),
			Handler: share.HandlerFunc(func(_ context.Context, event share.Event, _ bool) (share.Event, events.LogResult, error) {
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
	if _, _, err := active.Run(context.Background(), share.Event{Point: share.PointAgentInputPrepared}, CallOptions{Woken: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(runs) != 2 || runs[0] != "new" || runs[1] != "other" {
		t.Fatalf("runs = %#v, want new and other", runs)
	}
}
