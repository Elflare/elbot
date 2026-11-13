package elnis

import (
	"context"
	"strings"
	"testing"

	background "elbot/internal/background/share"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/operationresult"
)

type takeoverRunner struct {
	calls int
	err   error
}

func (r *takeoverRunner) RunBackground(ctx context.Context, _ background.RunRequest) (context.Context, backgroundRunResult, error) {
	r.calls++
	outcome := "completed"
	if r.err != nil {
		outcome = "cancelled"
	}
	if r.err != nil {
		return contextinfo.WithExecution(ctx, contextinfo.Execution{RunID: "logical-run", SessionID: "adopted"}), backgroundRunResult{}, r.err
	}
	return ctx, successfulBackgroundRun(background.RunResult{RunID: "logical-run", SessionID: "adopted", MessageID: "actual-answer", Text: "ordinary foreground answer", TakenOver: true, Outcome: outcome}), nil
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
			svc.sessionTakenOver = func(_ context.Context, id string) (bool, error) { return id == "adopted", nil }
			var queued QueuedLLMEvent
			svc.SetLLMEnqueuer(func(_ context.Context, event QueuedLLMEvent) error { queued = event; return nil })
			if _, err := svc.Handle(context.Background(), "secret", testRequest(ModeLLM)); err != nil {
				t.Fatal(err)
			}
			if err := svc.RunLLMEvent(context.Background(), queued.Event, queued.EventID); err != nil {
				t.Fatalf("error: %v", err)
			}
			record, err := testRepo(svc).snapshot(context.Background(), queued.EventID)
			if err != nil {
				t.Fatal(err)
			}
			if record.Status != StatusTakenOver || record.SessionID != "adopted" || !strings.Contains(record.Result, "logical-run") || !strings.Contains(record.Result, "taken_over") {
				t.Fatalf("record: %#v", record)
			}
			if runner.calls != 1 || sent != 0 {
				t.Fatalf("calls=%d sent=%d", runner.calls, sent)
			}
			if _, result, err := svc.deliverReport(context.Background(), queued.EventID); err != nil || result.Status != operationresult.Success {
				t.Fatalf("deliverReport result=%+v err=%v", result, err)
			}
			if sent != 0 {
				t.Fatal("taken over event delivered report")
			}
		})
	}
}
