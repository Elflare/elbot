package elnis

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/signal"
)

func TestFormatRetryExecutionFailureKeepsOriginalCause(t *testing.T) {
	for _, failure := range []error{errors.New("model failed"), errors.New("save failed"), context.Canceled} {
		t.Run(failure.Error(), func(t *testing.T) {
			runner := &fakeBackgroundRunner{text: "invalid json", errors: []error{nil, failure}}
			sent := 0
			service, cleanup := newTestServiceWithRunner(t, runner, func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error) {
				sent++
				return delivery.Receipt{}, nil
			})
			defer cleanup()
			var queued QueuedLLMEvent
			service.SetLLMEnqueuer(func(_ context.Context, event QueuedLLMEvent) error { queued = event; return nil })
			if _, err := service.Handle(t.Context(), "secret", testRequest(ModeLLM)); err != nil {
				t.Fatal(err)
			}
			var records []events.LogRecord
			connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
				if record.EventName == "elnis_llm_failed" {
					records = append(records, record)
				}
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Disconnect()
			if err := service.RunLLMEvent(t.Context(), queued.Event, queued.EventID); err != nil {
				t.Fatal(err)
			}
			row, err := testRepo(service).snapshot(t.Context(), queued.EventID)
			if err != nil || row.Status != StatusFailed || len(runner.requests) != 2 || sent != 0 || len(records) != 2 {
				t.Fatalf("row=%+v err=%v calls=%d sent=%d logs=%+v", row, err, len(runner.requests), sent, records)
			}
			var detail struct {
				Operation string `json:"operation"`
				Error     string `json:"error"`
			}
			if err := json.Unmarshal([]byte(records[1].Content), &detail); err != nil {
				t.Fatal(err)
			}
			if detail.Operation != "run_model" || detail.Error != failure.Error() || strings.Contains(row.Result, "解析格式失败") {
				t.Fatalf("execution failure reclassified: %+v row=%+v", detail, row)
			}
		})
	}
}
