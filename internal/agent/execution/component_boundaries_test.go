package execution

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"errors"
	"sync"
	"testing"
	"time"

	"elbot/internal/agent/dialogue"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/modelmgr"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/turn"
	"elbot/internal/workspace"
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
	recorder := NewStatusRecorder(StatusRecorderDeps{View: dialogue.ExecutionView{}, Turns: turns, Changed: signal.New[StatusChangedEvent, signal.Empty]("test.status")})
	t.Cleanup(func() { _ = recorder.changed.Close(context.Background()) })
	started := time.Now()
	recorder.Record(contexts["s1"], runtimestatus.Snapshot{SessionID: "s1", Model: "model", TurnStartedAt: started, Phase: runtimestatus.PhaseLLM})
	p := &statusReadingPlatform{recorder: recorder, observed: make(chan runtimestatus.Snapshot, 1)}
	connection, err := recorder.changed.Observe(func(ctx context.Context, event StatusChangedEvent) error {
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

func TestExecutionViewKeepsRequestCancellationAndClearsBackgroundOverrides(t *testing.T) {
	execution := turn.NewExecution("run")
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	ctx := turn.WithAttempt(turn.WithExecution(requestCtx, execution), "attempt")
	ctx = agentshare.WithMessageContext(contextinfo.WithConversation(ctx, contextinfo.Conversation{Source: contextinfo.Source{Platform: "qq", ScopeID: "cron:1"}}), agentshare.MessageContext{

		BufferAssistantOutput: true,
	})
	ctx = workspace.WithPathScope(ctx, workspace.PathScope{Dir: "/background/task", Restricted: true})
	ctx = contextinfo.WithBackgroundKind(ctx, contextinfo.BackgroundKindCron)
	ctx = modelmgr.WithSelectionOverride(ctx, config.ModelSelection{Provider: "task", Model: "task"})
	foreground, cancelForeground := context.WithCancel(contextinfo.WithConversation(context.Background(), contextinfo.Conversation{Source: contextinfo.Source{Platform: "cli", ScopeID: "remote:original"}}))
	actor := contextinfo.Actor{ID: "cli:owner", Role: contextinfo.RoleSuperadmin}
	execution.Adopt(contextinfo.WithActor(foreground, actor), storage.Session{})
	cancelForeground()
	view := dialogue.ExecutionView{}
	refreshed := view.Context(ctx)
	msg, ok := agentshare.MessageContextFrom(refreshed)
	if ok || msg.BufferAssistantOutput {
		t.Fatalf("retained background routing: %+v", msg)
	}
	if info, ok := contextinfo.ConversationFromContext(refreshed); !ok || info.Source.ScopeID != "remote:original" {
		t.Fatalf("foreground public facts = %+v, %v", info, ok)
	}
	if scope, ok := workspace.PathScopeFromContext(refreshed); ok {
		t.Fatalf("retained path scope: %+v", scope)
	}
	if contextinfo.BackgroundContext(refreshed) {
		t.Fatal("retained background identity")
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
