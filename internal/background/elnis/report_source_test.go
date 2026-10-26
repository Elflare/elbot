package elnis

import (
	"context"
	"elbot/internal/events"
	"testing"

	"elbot/internal/delivery"
)

func TestDefaultAdminReportUsesActualReceiptSources(t *testing.T) {
	ctx := context.Background()
	runner := &fakeBackgroundRunner{text: `{"completed":true,"need_report":true,"report":"done"}`}
	s, cleanup := newTestServiceWithRunner(t, runner, func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error) {
		return delivery.Receipt{
			PlatformMessageIDs: []string{"admin-1", "admin-2"},
			SentMessages: []delivery.SentMessage{
				{Platform: "qqonebot", ScopeID: "private:1001", PlatformMessageID: "admin-1", OutputIndexes: []int{0}},
				{Platform: "qqonebot", ScopeID: "private:1002", PlatformMessageID: "admin-2", OutputIndexes: []int{0}},
			},
		}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
	})
	defer cleanup()
	var queued QueuedLLMEvent
	s.SetLLMEnqueuer(func(_ context.Context, e QueuedLLMEvent) error { queued = e; return nil })
	req := testRequest(ModeLLM)
	req.Targets = []Target{{Platform: "qqonebot"}}
	if _, err := s.Handle(ctx, "secret", req); err != nil {
		t.Fatal(err)
	}
	if err := s.RunLLMEvent(ctx, queued.Event, queued.EventID); err != nil {
		t.Fatal(err)
	}
	for _, sent := range []struct{ scope, id string }{{"private:1001", "admin-1"}, {"private:1002", "admin-2"}} {
		assertReportMapping(t, s, "qqonebot", sent.scope, sent.id)
	}
}
