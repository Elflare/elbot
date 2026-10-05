package elnis

import (
	"context"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/storage"
)

func TestDefaultAdminReportUsesActualReceiptSources(t *testing.T) {
	ctx := context.Background()
	runner := &fakeBackgroundRunner{text: `{"completed":true,"need_report":true,"report":"done"}`}
	s, cleanup := newTestServiceWithRunner(t, runner, func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error) {
		return delivery.Receipt{
			PlatformMessageIDs: []string{"admin-1", "admin-2"},
			SentMessages: []delivery.SentMessage{
				{Platform: "qqonebot", ScopeID: "private:1001", PlatformMessageID: "admin-1", OutputIndexes: []int{0}},
				{Platform: "qqonebot", ScopeID: "private:1002", PlatformMessageID: "admin-2", OutputIndexes: []int{0}},
			},
		}, nil
	})
	defer cleanup()
	row := &storage.Session{ID: "bg-session", OwnerID: "elnis:home", Platform: "qqonebot", PlatformScopeID: "elnis:review", Mode: storage.SessionModeBackground, Status: storage.SessionStatusActive}
	if err := s.store.Sessions().Create(ctx, row); err != nil {
		t.Fatal(err)
	}
	if err := s.store.Messages().Append(ctx, &storage.Message{ID: "bg-message", SessionID: row.ID, Role: storage.RoleAssistant, Content: "done"}); err != nil {
		t.Fatal(err)
	}
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
		msg, err := s.store.Messages().FindByPlatformMessage(ctx, "qqonebot", sent.scope, sent.id)
		if err != nil || msg.ID != "bg-message" {
			t.Fatalf("mapping %s/%s: %#v/%v", sent.scope, sent.id, msg, err)
		}
	}
}
