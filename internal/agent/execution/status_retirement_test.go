package execution

import (
	"context"
	"elbot/internal/agent/dialogue"
	"elbot/internal/llm"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/turn"
	"encoding/json"
	"sync"
	"testing"
)

func TestStatusRetirementRejectsLatePublicationAndIdleCacheRecreation(t *testing.T) {
	turns := turn.NewManager()
	changed := signal.New[StatusChangedEvent, signal.Empty]("retirement")
	defer changed.Close(context.Background())
	r := NewStatusRecorder(StatusRecorderDeps{View: dialogue.ExecutionView{}, Turns: turns, Changed: changed})
	e := turn.NewExecution("old")
	ctx := turn.WithAttempt(turn.WithExecution(t.Context(), e), "old-attempt")
	turns.StartExecution("s", turn.Input{}, e, "old-attempt")
	r.Record(ctx, runtimestatus.Snapshot{SessionID: "s", Phase: runtimestatus.PhaseLLM})
	turns.FinishRequest("s", "old-attempt")
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 50 {
				r.Record(ctx, runtimestatus.Snapshot{SessionID: "s", Phase: runtimestatus.PhaseDone})
			}
		})
	}
	r.ForgetSession("s")
	wg.Wait()
	r.ForgetSession("s")
	if len(r.snapshots) != 0 || len(r.owners) != 0 {
		t.Fatal("retired state was retained or recreated")
	}
	tokens := int64(42)
	metadata, err := json.Marshal(map[string]any{"last_usage": &llm.Usage{TotalTokens: &tokens}})
	if err != nil {
		t.Fatal(err)
	}
	row := &storage.Session{ID: "s", Metadata: string(metadata)}
	usage, err := r.Usage(row)
	if err != nil || usage.TotalTokens == nil || *usage.TotalTokens != 42 {
		t.Fatal(usage, err)
	}
	if len(r.snapshots) != 0 {
		t.Fatal("idle status query recreated the cache")
	}
	next := turn.NewExecution("next")
	turns.StartExecution("s", turn.Input{}, next, "next-attempt")
	if _, err := r.Usage(row); err != nil {
		t.Fatal(err)
	}
	nextCtx := turn.WithAttempt(turn.WithExecution(t.Context(), next), "next-attempt")
	r.Record(nextCtx, runtimestatus.Snapshot{SessionID: "s", Phase: runtimestatus.PhaseLLM})
	r.Record(ctx, runtimestatus.Snapshot{SessionID: "s", Phase: runtimestatus.PhaseError})
	if r.Snapshot("s").Phase != runtimestatus.PhaseLLM || *r.Snapshot("s").Usage.TotalTokens != 42 {
		t.Fatal("old execution contaminated resumed state")
	}
}

func TestStatusRetirementDoesNotWaitForDisplayCallback(t *testing.T) {
	turns := turn.NewManager()
	changed := signal.New[StatusChangedEvent, signal.Empty]("retirement-blocked")
	defer changed.Close(context.Background())
	r := NewStatusRecorder(StatusRecorderDeps{View: dialogue.ExecutionView{}, Turns: turns, Changed: changed})
	e := turn.NewExecution("old")
	ctx := turn.WithAttempt(turn.WithExecution(t.Context(), e), "attempt")
	turns.StartExecution("s", turn.Input{}, e, "attempt")
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	_, err := changed.Observe(func(context.Context, StatusChangedEvent) error { close(started); <-release; return nil }, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		r.Record(ctx, runtimestatus.Snapshot{SessionID: "s", Phase: runtimestatus.PhaseDone})
		close(done)
	}()
	<-started
	turns.FinishRequest("s", "attempt")
	r.ForgetSession("s")
	close(release)
	<-done
	if len(r.snapshots) != 0 || len(r.owners) != 0 {
		t.Fatal("publication recreated released state")
	}
}
