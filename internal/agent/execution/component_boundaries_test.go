package execution

import (
	"context"
	"errors"

	"sync"
	"testing"
	"time"

	"elbot/internal/agent/dialogue"
	agentevents "elbot/internal/agent/events"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/hook"

	"elbot/internal/modelmgr"
	"elbot/internal/platform"
	runtimestatus "elbot/internal/runtime"
	sandboxctx "elbot/internal/sandbox"

	"elbot/internal/signal"
	"elbot/internal/storage"

	"elbot/internal/turn"
)

type statusReadingPlatform struct {
	fakePlatform
	recorder *StatusRecorder
	observed chan runtimestatus.Snapshot
}

func (p *statusReadingPlatform) SetRuntimeStatus(_ context.Context, sent runtimestatus.Snapshot) error {
	p.observed <- p.recorder.Snapshot(sent.SessionID)
	return errors.New("display unavailable")
}

func TestComponentStatusCommittedBeforeObserversAndCancellation(t *testing.T) {
	turns := turn.NewManager()
	contexts := map[string]context.Context{}
	for _, id := range []string{"s1", "s2", "s3"} {
		execution := turn.NewExecution(id)
		contexts[id] = turn.WithAttempt(turn.WithExecution(t.Context(), execution), "attempt")
		turns.StartExecution(id, turn.Input{}, execution, "attempt")
	}
	recorder := NewStatusRecorder(StatusRecorderDeps{Turns: turns, Changed: signal.New[agentevents.StatusChangedEvent, signal.Empty]("test.status")})
	t.Cleanup(func() { _ = recorder.changed.Close(context.Background()) })
	started := time.Now()
	recorder.Record(contexts["s1"], runtimestatus.Snapshot{SessionID: "s1", Model: "model", TurnStartedAt: started, Phase: runtimestatus.PhaseLLM})
	p := &statusReadingPlatform{recorder: recorder, observed: make(chan runtimestatus.Snapshot, 1)}
	connection, err := recorder.changed.Observe(func(ctx context.Context, event agentevents.StatusChangedEvent) error {
		if ctx.Err() != nil {
			t.Error("committed fact inherited cancellation")
		}
		return p.SetRuntimeStatus(ctx, event.Snapshot)
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(contexts["s1"])
	cancel()
	recorder.Record(ctx, runtimestatus.Snapshot{SessionID: "s1", Phase: runtimestatus.PhaseDone})
	got := <-p.observed
	if got.Phase != runtimestatus.PhaseDone || got.Model != "model" || !got.TurnStartedAt.Equal(started) {
		t.Fatalf("stale snapshot: %+v", got)
	}
	if recorder.Snapshot("s1").Phase != runtimestatus.PhaseDone {
		t.Fatal("observer failure changed committed state")
	}
	if err := connection.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, id := range []string{"s1", "s2", "s3"} {
		wg.Go(func() {
			for range 30 {
				recorder.Record(contexts[id], runtimestatus.Snapshot{SessionID: id, Phase: runtimestatus.PhaseLLM})
				_ = recorder.Snapshot(id)
			}
		})
	}
	wg.Wait()
}

func TestHookFailureIsRecordedOnceAndStillNotifies(t *testing.T) {
	manager := newTestHookManager()

	manager.Register(hook.Registration{Point: hook.PointErrorOccurred, Name: "failing", Match: hook.Always(), Handler: hook.HandlerFunc(func(_ context.Context, event hook.Event) (hook.Event, globalevents.LogResult, error) {
		return event, globalevents.LogResult{ResultStatus: globalevents.ResultFailed, Content: "test source failed"}, errors.New("hook failure")
	})})
	records := make(chan globalevents.LogRecord, 32)
	connection, _ := globalevents.LogSubmitted.Observe(func(_ context.Context, record globalevents.LogRecord) error { records <- record; return nil }, signal.ConnectOptions{})
	defer connection.Disconnect()
	failed := agentevents.NewSignals().HookFailed
	bridge := newTestHookBridge(t, &fakePlatform{}, manager, failed)
	facts := 0
	_, _ = failed.Observe(func(context.Context, agentevents.HookFailedEvent) error { facts++; return nil }, signal.ConnectOptions{})
	bridge.Notify(context.Background(), hook.Event{Point: hook.PointErrorOccurred})
	count := 0
	for len(records) > 0 {
		r := <-records
		if r.EventName == "hook_error" {
			count++
			if r.Module != "hook" {
				t.Fatal(r)
			}
		}
	}
	if count != 1 || facts != 1 {
		t.Fatalf("original failure records=%d facts=%d", count, facts)
	}
}

func TestExecutionViewKeepsRequestCancellationAndClearsBackgroundOverrides(t *testing.T) {
	execution := turn.NewExecution("run")
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	ctx := turn.WithAttempt(turn.WithExecution(requestCtx, execution), "attempt")
	ctx = platform.WithMessageContext(ctx, platform.MessageContext{
		Conversation:          contextinfo.Conversation{Source: contextinfo.Source{Platform: "qq", ScopeID: "cron:1"}},
		Sender:                &fakePlatform{},
		BufferAssistantOutput: true,
	})
	ctx = sandboxctx.WithSandboxContext(ctx, sandboxctx.SandboxContext{Root: "/background", Dir: "/background/task"})
	ctx = modelmgr.WithSelectionOverride(ctx, config.ModelSelection{Provider: "task", Model: "task"})
	foreground, cancelForeground := context.WithCancel(contextinfo.WithConversation(context.Background(), contextinfo.Conversation{Source: contextinfo.Source{Platform: "cli", ScopeID: "remote:original"}}))
	actor := contextinfo.Actor{ID: "cli:owner", Role: contextinfo.RoleSuperadmin}
	execution.Adopt(contextinfo.WithActor(foreground, actor), storage.Session{})
	cancelForeground()
	view := dialogue.ExecutionView{}
	refreshed := view.Context(ctx)
	msg, ok := platform.MessageContextFrom(refreshed)
	if ok || msg.Sender != nil || msg.BufferAssistantOutput {
		t.Fatalf("retained background routing: %+v", msg)
	}
	if info, ok := contextinfo.ConversationFromContext(refreshed); !ok || info.Source.ScopeID != "remote:original" {
		t.Fatalf("foreground public facts = %+v, %v", info, ok)
	}
	if sandbox, _ := sandboxctx.SandboxContextFromContext(refreshed); sandbox.Root != "" || sandbox.Dir != "" {
		t.Fatalf("retained sandbox: %+v", sandbox)
	}
	if selection := modelmgr.SelectionOverrideFromContext(refreshed); selection.Provider != "" || selection.Model != "" {
		t.Fatalf("retained model override: %+v", selection)
	}
	if got, _ := contextinfo.ActorFromContext(refreshed); got != actor || turn.AttemptFromContext(refreshed) != "attempt" || refreshed.Err() != nil {
		t.Fatalf("lost request identity or inherited foreground cancellation: %+v / %v", got, refreshed.Err())
	}
	cancelRequest()
	if !errors.Is(refreshed.Err(), context.Canceled) || !errors.Is(view.Context(ctx).Err(), context.Canceled) {
		t.Fatal("execution view detached request cancellation")
	}
}
