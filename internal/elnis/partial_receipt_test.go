package elnis

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/storage"
)

func TestPartialReportMapsSuccessAndRemainsRetryable(t *testing.T) {
	ctx := context.Background()
	wantErr := errors.New("later page failed")
	calls := 0
	runner := &fakeBackgroundRunner{text: `{"completed":true,"need_report":true,"report":"done"}`}
	s, cleanup := newTestServiceWithRunner(t, runner, func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error) {
		calls++
		if calls == 1 {
			return delivery.Receipt{PlatformMessageIDs: []string{"partial"}, SentMessages: []delivery.SentMessage{{Platform: "qqonebot", ScopeID: "private:1001", PlatformMessageID: "partial"}}}, wantErr
		}
		return delivery.Receipt{PlatformMessageIDs: []string{"retry"}, SentMessages: []delivery.SentMessage{{Platform: "qqonebot", ScopeID: "private:1001", PlatformMessageID: "retry"}}}, nil
	})
	defer cleanup()
	row := &storage.Session{ID: "bg-session", OwnerID: "elnis:home", Platform: "qqonebot", PlatformScopeID: "elnis:watcher:source:event-1", Mode: storage.SessionModeWork, Status: storage.SessionStatusActive}
	if err := s.store.Sessions().Create(ctx, row); err != nil {
		t.Fatal(err)
	}
	if err := s.store.Messages().Append(ctx, &storage.Message{ID: "bg-message", SessionID: row.ID, Role: storage.RoleAssistant, Content: "done"}); err != nil {
		t.Fatal(err)
	}
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
	msg, err := s.store.Messages().FindByPlatformMessage(ctx, "qqonebot", "private:1001", "partial")
	if err != nil || msg.ID != "bg-message" {
		t.Fatalf("mapping=%#v/%v", msg, err)
	}
	event, err := s.store.ElnisEvents().Get(ctx, queued.EventID)
	if err != nil || event.Status != StatusResultReady {
		t.Fatalf("event=%#v/%v", event, err)
	}
	if err := s.recoverReports(ctx, false); err != nil {
		t.Fatal(err)
	}
	event, err = s.store.ElnisEvents().Get(ctx, queued.EventID)
	if err != nil || event.Status != StatusCompleted || calls != 2 {
		t.Fatalf("recovery=%#v/%v calls=%d", event, err, calls)
	}
}
