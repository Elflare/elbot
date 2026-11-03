package elnis

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/delivery"
)

func TestPartialReportMapsSuccessAndRemainsRetryable(t *testing.T) {
	ctx := context.Background()
	wantErr := errors.New("later page failed")
	calls := 0
	runner := &fakeBackgroundRunner{text: `{"completed":true,"need_report":true,"report":"done"}`}
	s, cleanup := newTestServiceWithRunner(t, runner, func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error) {
		calls++
		if calls == 1 {
			return delivery.Receipt{PlatformMessageIDs: []string{"partial"}, SentMessages: []delivery.SentMessage{{Platform: "qqonebot", ChatType: "private", ChatID: "1001", PlatformMessageID: "partial"}}}, wantErr
		}
		return delivery.Receipt{PlatformMessageIDs: []string{"retry"}, SentMessages: []delivery.SentMessage{{Platform: "qqonebot", ChatType: "private", ChatID: "1001", PlatformMessageID: "retry"}}}, nil
	})
	defer cleanup()
	var queued QueuedLLMEvent
	s.SetLLMEnqueuer(func(_ context.Context, event QueuedLLMEvent) error { queued = event; return nil })
	req := testRequest(ModeLLM)
	req.Targets = []Target{{Platform: "qqonebot", Type: "private", ID: "1001"}}
	if _, err := s.Handle(ctx, "secret", req); err != nil {
		t.Fatal(err)
	}
	if err := s.RunLLMEvent(ctx, queued.Event, queued.EventID); !errors.Is(err, wantErr) {
		t.Fatalf("send error=%v", err)
	}
	assertReportMapping(t, s, "qqonebot", "private", "1001", "partial")
	event, err := testRepo(s).snapshot(ctx, queued.EventID)
	if err != nil || event.Status != StatusResultReady {
		t.Fatalf("event=%#v/%v", event, err)
	}
	if err := s.recoverReports(ctx); err != nil {
		t.Fatal(err)
	}
	event, err = testRepo(s).snapshot(ctx, queued.EventID)
	if err != nil || event.Status != StatusCompleted || calls != 2 {
		t.Fatalf("recovery=%#v/%v calls=%d", event, err, calls)
	}
}
