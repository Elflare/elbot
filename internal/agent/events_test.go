package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/llm"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/signal"
	"elbot/internal/turn"
)

func TestStatusRejectsOldAttemptsAndCopiesUsage(t *testing.T) {
	turns := turn.NewManager()
	events := signal.New[StatusChangedEvent]("status", nil)
	var observed []StatusChangedEvent
	_, _ = events.Connect(func(_ context.Context, e StatusChangedEvent) error { observed = append(observed, e); return nil }, signal.ConnectOptions{})
	r := &statusRecorder{turns: turns, changed: events}
	old, current := turn.NewExecution("old"), turn.NewExecution("current")
	oldCtx := turn.WithAttempt(turn.WithExecution(context.Background(), old), "old-attempt")
	ctx := turn.WithAttempt(turn.WithExecution(context.Background(), current), "current-attempt")
	turns.StartExecution("s", turn.Input{}, old, "old-attempt")
	r.Record(oldCtx, runtimestatus.Snapshot{SessionID: "s", Phase: runtimestatus.PhaseLLM}, true)
	turns.StopSession("s", "old-attempt")
	turns.StartExecution("s", turn.Input{}, current, "current-attempt")
	usage := &llm.Usage{TotalTokens: 42}
	r.Record(ctx, runtimestatus.Snapshot{SessionID: "s", Phase: runtimestatus.PhaseTool, Usage: usage}, true)
	r.Record(oldCtx, runtimestatus.Snapshot{SessionID: "s", Phase: runtimestatus.PhaseError}, true)
	usage.TotalTokens = 900
	copy := r.Snapshot("s")
	copy.Usage.TotalTokens = 901
	if len(observed) != 2 || observed[1].Version != observed[0].Version+1 || observed[1].Snapshot.Usage.TotalTokens != 42 || r.Snapshot("s").Usage.TotalTokens != 42 {
		t.Fatalf("old attempt/snapshot leaked: %+v", observed)
	}
	turns.StopSession("s", "current-attempt")
	r.Record(ctx, runtimestatus.Snapshot{SessionID: "s", Phase: runtimestatus.PhaseDone}, true)
	if r.Snapshot("s").Phase != runtimestatus.PhaseDone {
		t.Fatal("matching terminal rejected after turn cleanup")
	}
	r.Record(oldCtx, runtimestatus.Snapshot{SessionID: "s", Phase: runtimestatus.PhaseDone}, true)
	if len(observed) != 3 {
		t.Fatal("old terminal accepted")
	}
}

func TestModelCompletionSnapshotAndObserverFailureDoNotChangeResult(t *testing.T) {
	usage := &llm.Usage{TotalTokens: 7}
	a := newTestAgent(t, &fakePlatform{}, &fakeLLM{chunks: [][]llm.StreamChunk{{{DeltaContent: "source", Usage: usage}}}}, "model", config.ProviderConfig{}, newTestStore(t))
	var event ModelCallCompletedEvent
	_, _ = a.Signals().ModelCallCompleted.Connect(func(_ context.Context, e ModelCallCompletedEvent) error {
		event = e
		return errors.New("observer failed")
	}, signal.ConnectOptions{})
	ctx, row, err := a.execution.resolveInput(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	out := foregroundTurnOutput{sender: a.output, status: a.status}
	result, err := a.caller.Call(ctx, row, modelSelectionForTurn(ctx, a.models, row), nil, nil, nil, nil, out)
	if err != nil || result.Text != "source" || !event.OutputReady {
		t.Fatalf("observation changed result: %+v / %v", result, err)
	}
	usage.TotalTokens = 100
	result.Usage.TotalTokens = 101
	if event.Usage.TotalTokens != 7 {
		t.Fatal("model fact retained mutable usage")
	}
}

func TestReplyFactsPreservePartialSuccessAndReceiptSnapshot(t *testing.T) {
	f := newReplyTestFixture(t, false)
	f.platform.sendErr = errors.New("partial send")
	f.repo.mapErr = errors.New("map failed")
	f.platform.receipt.SentMessages[0].OutputIndexes = []int{0}
	signals := newSignals()
	f.committer.delivered, f.committer.committed = signals.ReplyDelivered, signals.ReplyCommitted
	var delivered ReplyDeliveredEvent
	var committed ReplyCommittedEvent
	_, _ = signals.ReplyDelivered.Connect(func(_ context.Context, e ReplyDeliveredEvent) error { delivered = e; return nil }, signal.ConnectOptions{})
	_, _ = signals.ReplyCommitted.Connect(func(_ context.Context, e ReplyCommittedEvent) error { committed = e; return nil }, signal.ConnectOptions{})
	result, err := f.committer.Commit(f.ctx, f.requestCtx, f.in, f.out)
	if err == nil || !result.Persisted || !committed.Persisted || delivered.Err == nil || len(committed.AssociationErrors) != 1 {
		t.Fatalf("partial facts lost: %+v / %+v / %v", delivered, committed, err)
	}
	f.platform.receipt.PlatformMessageIDs[0] = "changed"
	result.Receipt.SentMessages[0].OutputIndexes[0] = 99
	if delivered.Receipt.PlatformMessageIDs[0] != "sent" || committed.Receipt.SentMessages[0].OutputIndexes[0] != 0 {
		t.Fatal("receipt snapshot mutated")
	}
}

func TestConfirmationPublishesAfterAdmissionRelease(t *testing.T) {
	a := newTestAgent(t, &fakePlatform{}, &fakeLLM{}, "model", config.ProviderConfig{}, newTestStore(t))
	ctx, row, err := a.execution.resolveInput(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	a.turns.StartLLM(row.ID, "hello")
	a.turns.StartToolPhase(row.ID)
	finished := make(chan struct{})
	go func() {
		_, _ = a.turns.AwaitRiskConfirmationContext(ctx, row.ID, turn.RiskConfirmation{ID: "c", ToolName: "tool"}, time.Second)
		close(finished)
	}()
	deadline := time.Now().Add(time.Second)
	for {
		if _, ok := a.turns.PendingRiskConfirmation(row.ID); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("confirmation not installed")
		}
		time.Sleep(time.Millisecond)
	}
	_, _ = a.Signals().ConfirmationChanged.Connect(func(_ context.Context, event ConfirmationChangedEvent) error {
		if event.Phase != "command" {
			return nil
		}
		lockCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, release, err := a.sessions.EnterActivation(lockCtx, a.identity.Scope(ctx), row.ID)
		if err != nil {
			t.Error("confirmation event emitted with admission held:", err)
			return err
		}
		release()
		return nil
	}, signal.ConnectOptions{})
	if err := a.confirmations.SubmitResponse(ctx, row.ID, "/confirm"); err != nil {
		t.Fatal(err)
	}
	<-finished
}
