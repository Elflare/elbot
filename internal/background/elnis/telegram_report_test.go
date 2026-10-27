package elnis

import (
	"context"
	"elbot/internal/events"
	"testing"

	"elbot/internal/delivery"
)

func TestLLMTextReportMapsAdminReceipts(t *testing.T) {
	ctx := context.Background()
	runner := &fakeBackgroundRunner{text: `{"completed":true,"need_report":true,"report":"done"}`}
	s, cleanup := newTestServiceWithRunner(t, runner, func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
		if target.Platform != "telegram" || !target.Superadmins {
			t.Fatalf("target=%#v", target)
		}
		return delivery.Receipt{PlatformMessageIDs: []string{"77", "77"}, SentMessages: []delivery.SentMessage{
			{Platform: "telegram", ScopeID: "private:1", PlatformMessageID: "77"},
			{Platform: "telegram", ScopeID: "private:2", PlatformMessageID: "77"},
		}}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
	})
	defer cleanup()
	s.enabledPlatforms = append(s.enabledPlatforms, "telegram")
	var queued QueuedLLMEvent
	s.SetLLMEnqueuer(func(_ context.Context, e QueuedLLMEvent) error { queued = e; return nil })
	req := testRequest(ModeLLM)
	req.Targets = []Target{{Platform: "telegram"}}
	if _, err := s.Handle(ctx, "secret", req); err != nil {
		t.Fatal(err)
	}
	if err := s.RunLLMEvent(ctx, queued.Event, queued.EventID); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"private:1", "private:2"} {
		assertReportMapping(t, s, "telegram", scope, "77")
	}
	record, err := testRepo(s).snapshot(ctx, queued.EventID)
	if err != nil || record.Status != StatusCompleted {
		t.Fatalf("record=%#v err=%v", record, err)
	}
}
