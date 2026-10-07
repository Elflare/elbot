package elnis

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/delivery/dispatch"
	"elbot/internal/platform/telegram"
	"elbot/internal/storage"
)

func TestLLMTextReportMapsRealTelegramAdminReceipts(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"ok":true,"result":{"message_id":77}}`) }))
	defer server.Close()
	adapter := telegram.New(telegram.Config{BotToken: "token", APIBaseURL: server.URL, Format: "plain", Superadmins: []string{"1", "2"}}, nil, nil)
	router := dispatch.New(dispatch.Options{Primary: adapter})
	runner := &fakeBackgroundRunner{text: `{"completed":true,"need_report":true,"report":"done"}`}
	s, cleanup := newTestServiceWithRunner(t, runner, func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
		return router.SendNotice(ctx, delivery.Notice{Target: target, Outputs: outputs})
	})
	defer cleanup()
	s.enabledPlatforms = append(s.enabledPlatforms, "telegram")
	row := &storage.Session{ID: "bg-session", OwnerID: "elnis:home", Platform: "telegram", PlatformScopeID: "elnis:review", Mode: storage.SessionModeBackground, Status: storage.SessionStatusActive}
	if err := s.store.Sessions().Create(ctx, row); err != nil {
		t.Fatal(err)
	}
	if err := s.store.Messages().Append(ctx, &storage.Message{ID: "bg-message", SessionID: row.ID, Role: storage.RoleAssistant, Content: "done"}); err != nil {
		t.Fatal(err)
	}
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
		got, err := s.store.Messages().FindByPlatformMessage(ctx, "telegram", scope, "77")
		if err != nil || got.ID != "bg-message" {
			t.Fatalf("mapping %s=%#v/%v", scope, got, err)
		}
	}
	record, err := s.store.ElnisEvents().Get(ctx, queued.EventID)
	if err != nil || record.Status != StatusCompleted {
		t.Fatalf("record=%#v err=%v", record, err)
	}
}
