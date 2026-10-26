package agent

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/request"
)

func TestHookRequestsKeepPerHandlerIdentityAndCancellation(t *testing.T) {
	m := contractSupervisor(t, isolatedModuleDeps(t))
	parent, parentCtx, parentDone := m.requests.Start(t.Context(), request.StartRequest{SessionID: "session", Kind: request.KindTurn})
	defer parentDone()
	_, err := m.RegisterHooks(Points{AgentOutputPrepared: func(ctx context.Context, _ Facts, text string) (string, events.LogResult, error) {
		for _, name := range []string{"first", "second"} {
			runCtx, done := m.StartHookRequest(ctx, "session", name)
			facts, _ := contextinfo.ExecutionFromContext(runCtx)
			active := m.requests.ListBySession("session")
			found := false
			for _, r := range active {
				if r.ID == facts.RequestID {
					found = true
					if r.Kind != request.KindHook || r.ParentID != parent.ID || r.Label != name {
						t.Fatalf("request=%+v", r)
					}
				}
			}
			if !found || len(active) != 2 {
				t.Fatalf("requests=%+v", active)
			}
			if !m.requests.Cancel(facts.RequestID) || !errors.Is(runCtx.Err(), context.Canceled) {
				t.Fatal("handler cancellation lost")
			}
			if ctx.Err() != nil {
				t.Fatal("child cancellation canceled parent")
			}
			done()
		}
		return text, events.LogResult{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.bridge.AgentOutputPrepared(parentCtx, "text"); err != nil {
		t.Fatal(err)
	}
	if active := m.requests.ListBySession("session"); len(active) != 1 {
		t.Fatalf("handler request leaked: %+v", active)
	}
}

func TestBeginCloseRetainsCallbacksForDrainingProducers(t *testing.T) {
	m := contractSupervisor(t, isolatedModuleDeps(t))
	called := false
	_, err := m.RegisterHooks(Points{AgentOutputPrepared: func(_ context.Context, _ Facts, text string) (string, events.LogResult, error) {
		called = true
		return text, events.LogResult{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	m.BeginClose()
	if _, err := m.RegisterHooks(Points{}); err == nil {
		t.Fatal("registration accepted after BeginClose")
	}
	if _, _, err := m.bridge.AgentOutputPrepared(t.Context(), "final output"); err != nil || !called {
		t.Fatal("draining callback unavailable", err)
	}
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.bridge.AgentOutputPrepared(t.Context(), "too late"); err == nil {
		t.Fatal("callback accepted after Close")
	}
}
