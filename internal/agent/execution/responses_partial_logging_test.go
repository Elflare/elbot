package execution

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	agentevents "elbot/internal/agent/events"
	globalevents "elbot/internal/events"
	"elbot/internal/signal"
	"elbot/internal/storage"
)

type partialNativeStore struct {
	storage.Store
	items  *string
	status *string
}

func (s partialNativeStore) Dialogues() storage.DialogueRepository {
	return partialNativeRepository{DialogueRepository: s.Store.Dialogues(), items: s.items, status: s.status}
}

type partialNativeRepository struct {
	storage.DialogueRepository
	items  *string
	status *string
}

func (r partialNativeRepository) FinishExchange(ctx context.Context, id, status, response, items, failure string) error {
	*r.items, *r.status = items, status
	return r.DialogueRepository.FinishExchange(ctx, id, status, response, items, failure)
}

func TestResponsesPartialTextAndRefusalSurviveBeforeItemDone(t *testing.T) {
	var savedItems, savedStatus string
	f := newNativeFixture(t, func(_ int, _ nativeTestRequest, w http.ResponseWriter) {
		fmt.Fprint(w, "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"partial-message\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"future\":true}}\n\n")
		// Sparse and out-of-order indices must retain only received parts.
		fmt.Fprint(w, "event: response.refusal.delta\ndata: {\"type\":\"response.refusal.delta\",\"output_index\":0,\"content_index\":4,\"delta\":\"拒绝\"}\n\n")
		fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":1,\"delta\":\"部分\"}\n\n")
		fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":1,\"delta\":\"正文\"}\n\n")
	}, func(opts *testAgentOptions) {
		opts.Store = partialNativeStore{Store: opts.Store, items: &savedItems, status: &savedStatus}
	})
	var completed agentevents.ModelCallCompletedEvent
	connection, err := f.agent.Signals().ModelCallCompleted.Observe(func(_ context.Context, event agentevents.ModelCallCompletedEvent) error {
		completed = event
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	var outputLog globalevents.LogRecord
	logs, err := globalevents.LogSubmitted.Observe(func(_ context.Context, record globalevents.LogRecord) error {
		if record.EventName == "assistant_message" {
			outputLog = record
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Disconnect()
	if err := f.agent.HandleMessage(t.Context(), "input"); err == nil {
		t.Fatal("unfinished stream reported success")
	}
	if !completed.OutputReady || completed.Text != "部分正文拒绝" || completed.SourceText != completed.Text {
		t.Fatalf("partial business facts=%+v", completed)
	}
	if outputLog.ResultStatus != globalevents.ResultFailed || !strings.Contains(outputLog.Content, "部分正文拒绝") {
		t.Fatalf("partial log=%+v", outputLog)
	}
	if savedStatus == "completed" || !strings.Contains(savedItems, `"text":"部分正文"`) || !strings.Contains(savedItems, `"refusal":"拒绝"`) || !strings.Contains(savedItems, `"future":true`) {
		t.Fatalf("saved status=%s items=%s", savedStatus, savedItems)
	}
	row, err := f.agent.execution.sessions.Current(t.Context(), f.agent.Scope(t.Context()))
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := f.store.Dialogues().CurrentCheckpoint(t.Context(), row.ID)
	if err != nil || checkpoint != nil {
		t.Fatalf("partial checkpoint=%+v error=%v", checkpoint, err)
	}
}
