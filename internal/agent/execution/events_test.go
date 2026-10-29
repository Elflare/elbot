package execution

import (
	"context"
	"elbot/internal/agent/dialogue"
	"errors"
	"strings"
	"testing"

	globalevents "elbot/internal/events"
	"elbot/internal/llm"

	runtimestatus "elbot/internal/runtime"
	"elbot/internal/signal"
	"elbot/internal/turn"
)

func TestStatusRejectsOldAttemptsAndCopiesUsage(t *testing.T) {
	turns := turn.NewSupervisor()
	events := signal.New[StatusChangedEvent, signal.Empty]("status")
	var observed []StatusChangedEvent
	_, _ = events.Observe(func(_ context.Context, e StatusChangedEvent) error {
		observed = append(observed, e)
		return nil
	}, signal.ConnectOptions{})
	r := NewStatusRecorder(StatusRecorderDeps{View: dialogue.ExecutionView{}, Turns: turns, Changed: events})
	old, current := turn.NewExecution("old"), turn.NewExecution("current")
	oldCtx := turn.WithAttempt(turn.WithExecution(context.Background(), old), "old-attempt")
	ctx := turn.WithAttempt(turn.WithExecution(context.Background(), current), "current-attempt")
	turns.StartAttempt("s", turn.Input{}, old, "old-attempt")
	r.Record(oldCtx, runtimestatus.Snapshot{SessionID: "s", Phase: runtimestatus.PhaseLLM})
	turns.StopAttempt("s", "old-attempt")
	turns.FinishAttempt("s", "old-attempt")
	turns.StartAttempt("s", turn.Input{}, current, "current-attempt")
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
	turns.StopAttempt("s", "current-attempt")
	turns.FinishAttempt("s", "current-attempt")
	r.Record(ctx, runtimestatus.Snapshot{SessionID: "s", Phase: runtimestatus.PhaseDone})
	if r.Snapshot("s").Phase != runtimestatus.PhaseDone {
		t.Fatal("matching terminal rejected after turn cleanup")
	}
	r.Record(oldCtx, runtimestatus.Snapshot{SessionID: "s", Phase: runtimestatus.PhaseDone})
	if len(observed) != 3 {
		t.Fatal("old terminal accepted")
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
