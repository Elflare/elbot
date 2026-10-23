package execution

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	agentevents "elbot/internal/agent/events"
	"elbot/internal/config"
	globalevents "elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/llm/chatcompletions"
	"elbot/internal/modelmgr"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/signal"
	"elbot/internal/turn"
)

func TestStatusRejectsOldAttemptsAndCopiesUsage(t *testing.T) {
	turns := turn.NewManager()
	events := signal.New[agentevents.StatusChangedEvent, signal.Empty]("status")
	var observed []agentevents.StatusChangedEvent
	_, _ = events.Observe(func(_ context.Context, e agentevents.StatusChangedEvent) error {
		observed = append(observed, e)
		return nil
	}, signal.ConnectOptions{})
	r := NewStatusRecorder(StatusRecorderDeps{Turns: turns, Changed: events})
	old, current := turn.NewExecution("old"), turn.NewExecution("current")
	oldCtx := turn.WithAttempt(turn.WithExecution(context.Background(), old), "old-attempt")
	ctx := turn.WithAttempt(turn.WithExecution(context.Background(), current), "current-attempt")
	turns.StartExecution("s", turn.Input{}, old, "old-attempt")
	r.Record(oldCtx, runtimestatus.Snapshot{SessionID: "s", Phase: runtimestatus.PhaseLLM})
	turns.StopSession("s", "old-attempt")
	turns.FinishRequest("s", "old-attempt")
	turns.StartExecution("s", turn.Input{}, current, "current-attempt")
	usage := &llm.Usage{TotalTokens: func() *int64 { n := int64(42); return &n }()}
	r.RecordUsage(ctx, "s", usage)
	r.RecordUsage(oldCtx, "s", &llm.Usage{})
	r.Record(oldCtx, runtimestatus.Snapshot{SessionID: "s", Phase: runtimestatus.PhaseError})
	*usage.TotalTokens = 900
	copy := r.Snapshot("s")
	*copy.Usage.TotalTokens = 901
	if len(observed) != 2 || observed[1].Version != observed[0].Version+1 || (observed[1].Snapshot.Usage.TotalTokens == nil || *observed[1].Snapshot.Usage.TotalTokens != 42) || (r.Snapshot("s").Usage.TotalTokens == nil || *r.Snapshot("s").Usage.TotalTokens != 42) {
		t.Fatalf("old attempt/snapshot leaked: %+v", observed)
	}
	turns.StopSession("s", "current-attempt")
	turns.FinishRequest("s", "current-attempt")
	r.Record(ctx, runtimestatus.Snapshot{SessionID: "s", Phase: runtimestatus.PhaseDone})
	if r.Snapshot("s").Phase != runtimestatus.PhaseDone {
		t.Fatal("matching terminal rejected after turn cleanup")
	}
	r.Record(oldCtx, runtimestatus.Snapshot{SessionID: "s", Phase: runtimestatus.PhaseDone})
	if len(observed) != 3 {
		t.Fatal("old terminal accepted")
	}
}

func TestModelCompletionSnapshotAndObserverFailureDoNotChangeResult(t *testing.T) {
	usage := &llm.Usage{TotalTokens: func() *int64 { n := int64(7); return &n }()}
	a := newTestAgent(t, &fakePlatform{}, &fakeLLM{chunks: [][]chatcompletions.Chunk{{{DeltaContent: "source", Usage: usage}}}}, "model", config.ProviderConfig{}, newTestStore(t))
	var event agentevents.ModelCallCompletedEvent
	_, _ = a.Signals().ModelCallCompleted.Observe(func(_ context.Context, e agentevents.ModelCallCompletedEvent) error {
		event = e
		return errors.New("observer failed")
	}, signal.ConnectOptions{})
	ctx, row, err := a.execution.ResolveInput(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	out := foregroundTurnOutput{sender: a.output}
	result, err := testChatRoute(a).Caller.Call(ctx, row, modelmgr.SelectionForTurn(ctx, a.execution.models, row), nil, nil, nil, nil, out)
	if err != nil || result.Text != "source" || !event.OutputReady {
		t.Fatalf("observation changed result: %+v / %v", result, err)
	}
	*usage.TotalTokens = 100
	*result.Usage.TotalTokens = 101
	if event.Usage.TotalTokens == nil || *event.Usage.TotalTokens != 7 {
		t.Fatal("model fact retained mutable usage")
	}
}

func TestReplyFactsPreservePartialSuccessAndReceiptSnapshot(t *testing.T) {
	f := newReplyTestFixture(t, false)
	f.platform.sendErr = errors.New("partial send")
	f.repo.mapErr = errors.New("map failed")
	f.platform.receipt.SentMessages[0].OutputIndexes = []int{0}
	var records []globalevents.LogRecord
	connection, err := globalevents.LogSubmitted.Observe(func(_ context.Context, record globalevents.LogRecord) error {
		if record.EventName == "reply_delivered" {
			records = append(records, record)
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	result, err := f.committer.Commit(f.ctx, f.requestCtx, f.in, f.out)
	if err == nil || result.Persisted || result.SendErr == nil || !errors.Is(result.PersistErr, f.repo.mapErr) || len(records) != 1 || records[0].EventName != "reply_delivered" {
		t.Fatalf("partial facts lost: %+v / %+v / %v", result, records, err)
	}
	f.platform.receipt.PlatformMessageIDs[0] = "changed"
	result.Receipt.SentMessages[0].OutputIndexes[0] = 99
	for _, record := range records {
		if !strings.Contains(record.Content, "platform_message_id=sent") || !strings.Contains(record.Content, "output_indexes=[0]") {
			t.Fatalf("receipt log snapshot mutated: %+v", record)
		}
	}
}

func TestConfirmationPublishesAfterAdmissionRelease(t *testing.T) {
	a := newTestAgent(t, &fakePlatform{}, &fakeLLM{}, "model", config.ProviderConfig{}, newTestStore(t))
	ctx, row, err := a.execution.ResolveInput(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	a.execution.turns.StartLLM(row.ID, "hello")
	a.execution.turns.StartToolPhase(row.ID)
	finished := make(chan struct{})
	go func() {
		_, _ = a.execution.turns.AwaitRiskConfirmationContext(ctx, row.ID, turn.RiskConfirmation{ID: "c", ToolName: "tool"}, time.Second)
		close(finished)
	}()
	deadline := time.Now().Add(time.Second)
	for {
		if _, ok := a.execution.turns.PendingRiskConfirmation(row.ID); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("confirmation not installed")
		}
		time.Sleep(time.Millisecond)
	}
	connection, err := globalevents.LogSubmitted.Observe(func(_ context.Context, record globalevents.LogRecord) error {
		if record.EventName != "risk_confirmation_command" || record.Identity.SessionID == nil || *record.Identity.SessionID != row.ID {
			return nil
		}
		lockCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, release, err := a.execution.sessions.EnterActivation(lockCtx, a.identity.Scope(ctx), row.ID)
		if err != nil {
			t.Error("confirmation event emitted with admission held:", err)
			return err
		}
		release()
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	if err := a.confirmations.SubmitResponse(ctx, row.ID, "/confirm"); err != nil {
		t.Fatal(err)
	}
	<-finished
}
