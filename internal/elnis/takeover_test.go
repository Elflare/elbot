package elnis

import (
	"context"
	"errors"
	"strings"
	"testing"

	"elbot/internal/background"
	"elbot/internal/delivery"
)

type takeoverRunner struct {
	calls int
	err   error
}

func (r *takeoverRunner) RunBackground(context.Context, background.RunRequest) (background.RunResult, error) {
	r.calls++
	outcome := "completed"
	if r.err != nil {
		outcome = "cancelled"
	}
	return background.RunResult{RunID: "logical-run", SessionID: "adopted", MessageID: "actual-answer", Text: "ordinary foreground answer", TakenOver: true, Outcome: outcome}, r.err
}

func TestTakeoverSuppressesRepairAndReport(t *testing.T) {
	for _, runErr := range []error{nil, context.Canceled} {
		name := "completed"
		if runErr != nil {
			name = "cancelled"
		}
		t.Run(name, func(t *testing.T) {
			runner := &takeoverRunner{err: runErr}
			sent := 0
			svc, cleanup := newTestServiceWithRunner(t, runner, func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error) {
				sent++
				return delivery.Receipt{}, nil
			})
			defer cleanup()
			var queued QueuedLLMEvent
			svc.SetLLMEnqueuer(func(_ context.Context, event QueuedLLMEvent) error { queued = event; return nil })
			if _, err := svc.Handle(context.Background(), "secret", testRequest(ModeLLM)); err != nil {
				t.Fatal(err)
			}
			if err := svc.RunLLMEvent(context.Background(), queued.Event, queued.EventID); !errors.Is(err, runErr) {
				t.Fatalf("error: %v", err)
			}
			record, err := svc.store.ElnisEvents().Get(context.Background(), queued.EventID)
			if err != nil {
				t.Fatal(err)
			}
			if record.Status != StatusTakenOver || record.SessionID != "adopted" || !strings.Contains(record.Result, "logical-run") || !strings.Contains(record.Result, name) {
				t.Fatalf("record: %#v", record)
			}
			if runner.calls != 1 || sent != 0 {
				t.Fatalf("calls=%d sent=%d", runner.calls, sent)
			}
			if err := svc.deliverReport(context.Background(), queued.EventID); err != nil {
				t.Fatal(err)
			}
			if sent != 0 {
				t.Fatal("taken over event delivered report")
			}
		})
	}
}
