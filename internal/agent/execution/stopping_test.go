package execution

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"elbot/internal/agent/dialogue"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/config"
	"elbot/internal/events"
	"elbot/internal/hook"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/session"
	"elbot/internal/turn"
)

func awaitStoppingInputWait(t *testing.T, a *agentFixture) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		a.execution.appendWaits.mu.Lock()
		active := a.execution.appendWaits.active
		a.execution.appendWaits.mu.Unlock()
		if active != 0 {
			return
		}
		select {
		case <-deadline:
			t.Fatal("input did not wait for the stopping attempt")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestStopWaitsForActualExitBeforeProcessingNewInput(t *testing.T) {
	old := fakeLLMBlock{started: make(chan struct{}), release: make(chan struct{}), ignoreCancellation: true}
	next := fakeLLMBlock{started: make(chan struct{}), release: make(chan struct{})}
	var releaseOld, releaseNext sync.Once
	t.Cleanup(func() { releaseOld.Do(func() { close(old.release) }); releaseNext.Do(func() { close(next.release) }) })
	f := &fakeLLM{chatBlocks: []fakeLLMBlock{old, next}, replies: []string{"discarded", "new answer"}}
	var prepared atomic.Int32
	hooks := newTestHookManager()
	hooks.Register(hook.Registration{Point: hook.PointAgentInputPrepared, Name: "count preparation", Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, e hook.Event) (hook.Event, events.LogResult, error) {
		prepared.Add(1)
		return e, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
	})})
	a := newTestAgent(t, &fakePlatform{}, f, "model", config.ProviderConfig{}, newTestStore(t), func(opts *testAgentOptions) { opts.HookManager = hooks })
	ctx := context.Background()
	oldDone := make(chan error, 1)
	go func() { oldDone <- a.HandleMessage(ctx, "first") }()
	select {
	case <-old.started:
	case <-time.After(3 * time.Second):
		t.Fatal("old request did not start")
	}
	id := f.chatRequests()[0].SessionID
	if err := a.HandleMessage(ctx, "/stop"); err != nil {
		t.Fatal(err)
	}
	if got := a.execution.turns.Snapshot(id); got.Phase != turn.PhaseStopping {
		t.Fatalf("turn retired before actual exit: %+v", got)
	}
	for _, req := range a.execution.requests.ListBySession(id) {
		if !req.Stopping {
			t.Fatalf("request did not reflect cancellation: %+v", req)
		}
	}
	newDone := make(chan error, 1)
	go func() { newDone <- a.HandleMessage(ctx, "second") }()
	awaitStoppingInputWait(t, a)
	deadline := time.After(3 * time.Second)
	for a.execution.status.Snapshot(id).Phase != runtimestatus.PhaseStopping {
		select {
		case <-deadline:
			t.Fatal("runtime did not publish stopping")
		case <-time.After(time.Millisecond):
		}
	}
	if got := len(f.chatRequests()); got != 1 {
		t.Fatalf("new input started before actual exit: %d model calls", got)
	}
	releaseOld.Do(func() { close(old.release) })
	select {
	case <-next.started:
	case <-time.After(3 * time.Second):
		t.Fatal("waiting input did not start after exit")
	}
	select {
	case err := <-oldDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("old request did not finish")
	}
	if prepared.Load() != 2 {
		t.Fatalf("waiting input preparation repeated: %d", prepared.Load())
	}
	releaseNext.Do(func() { close(next.release) })
	select {
	case err := <-newDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("new input did not finish")
	}
	if snapshot := a.execution.status.Snapshot(id); snapshot.Phase != runtimestatus.PhaseDone {
		t.Fatalf("late cancellation overwrote terminal status: %+v", snapshot)
	}
}

func TestAttemptWaitEndsOnInputOrApplicationCancellation(t *testing.T) {
	for _, how := range []string{"input", "application"} {
		t.Run(how, func(t *testing.T) {
			a := newTestAgent(t, &fakePlatform{}, &fakeLLM{}, "model", config.ProviderConfig{}, newTestStore(t))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- a.execution.waitForAttempt(ctx, make(chan struct{})) }()
			awaitStoppingInputWait(t, a)
			if how == "input" {
				cancel()
			} else {
				a.execution.BeginClose()
			}
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("wait returned: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("wait survived cancellation")
			}
		})
	}
}

type stopAfterPreparationDialogue struct {
	ExecutionDialogue
	stop func(string)
}

func (d stopAfterPreparationDialogue) PrepareTurn(ctx context.Context, in dialogue.TurnInput) (*dialogue.PreparedTurn, error) {
	prepared, err := d.ExecutionDialogue.PrepareTurn(ctx, in)
	if err == nil {
		d.stop(in.Session.ID)
	}
	return prepared, err
}

func TestStopBeforeRootRegistrationDoesNotStartModel(t *testing.T) {
	model := &fakeLLM{}
	f := newExecutionFixture(t, model, newTestStore(t))
	f.execution.dialogue = stopAfterPreparationDialogue{ExecutionDialogue: f.execution.dialogue, stop: func(id string) { f.opts.Turns.StopSession(id) }}
	ctx, row, err := f.execution.ResolveInput(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	err = f.execution.Run(ctx, row, "hello", f.out)
	if err != nil || model.requestCount() != 0 {
		t.Fatalf("stopping preparation continued: model calls=%d err=%v", model.requestCount(), err)
	}
	if len(f.opts.Requests.ListBySession(row.ID)) != 0 || f.opts.Turns.Snapshot(row.ID).Phase != turn.PhaseIdle {
		t.Fatal("stopped preparation left registered work")
	}
}

func TestStoppingInputRevalidatesBindingAfterWait(t *testing.T) {
	model := &fakeLLM{}
	a := newTestAgent(t, &fakePlatform{}, model, "model", config.ProviderConfig{}, newTestStore(t))
	ctx, row, err := a.execution.ResolveInput(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	a.execution.turns.StartExecution(row.ID, turn.Input{}, turn.NewExecution("old"), "old-attempt")
	a.execution.turns.StopSession(row.ID)
	result := make(chan error, 1)
	go func() { _, err := a.execution.AcceptInput(ctx, row, "waiting input"); result <- err }()
	awaitStoppingInputWait(t, a)
	// Hold admission while finishing the old attempt and switching sessions, so
	// the waiting input necessarily observes the invalidated original binding.
	scope := a.identity.Scope(ctx)
	locked, release, err := a.execution.sessions.EnterActivation(ctx, scope, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.execution.turns.FinishRequest(row.ID, "old-attempt")
	_, err = a.execution.sessions.Create(locked, scope, session.CreateRequest{Title: "new current"})
	release()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, agentshare.ErrSessionBindingChanged) {
			t.Fatalf("stale input was not rejected: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stale input did not finish")
	}
	if model.requestCount() != 0 {
		t.Fatal("waiting input replayed into a different session")
	}
}
