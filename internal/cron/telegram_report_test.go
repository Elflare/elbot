package cron

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/delivery/dispatch"
	"elbot/internal/platform/telegram"
	"elbot/internal/storage"
)

func TestTextReportMapsRealTelegramPartialReceipt(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ChatID int64 `json:"chat_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.ChatID == 2 {
			fmt.Fprint(w, `{"ok":false,"error_code":400,"description":"failed"}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":77}}`)
	}))
	defer server.Close()
	adapter := telegram.New(telegram.Config{BotToken: "token", APIBaseURL: server.URL, Format: "plain", Superadmins: []string{"1", "2"}}, nil, nil, nil)
	store := newCronSQLiteStore(t)
	router := dispatch.New(dispatch.Options{Primary: adapter, Store: store})
	row := &storage.Session{ID: "task", OwnerID: "cli:local", Platform: "cli", PlatformScopeID: "cron:task", Mode: storage.SessionModeBackground, Status: storage.SessionStatusActive}
	if err := store.Sessions().Create(ctx, row); err != nil {
		t.Fatal(err)
	}
	if err := store.Messages().Append(ctx, &storage.Message{ID: "report", SessionID: row.ID, Role: storage.RoleAssistant, Content: "report"}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(Options{Store: store, SendTarget: func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
		return router.SendNotice(ctx, delivery.Notice{Target: target, Outputs: outputs})
	}})
	err := svc.sendOutputsToPlatformTarget(ctx, "task", "telegram", delivery.Target{Platform: "telegram", Superadmins: true}, []delivery.Output{delivery.Text("report")}, row.ID, "report")
	if err == nil {
		t.Fatal("expected second target failure")
	}
	got, err := store.Messages().FindByPlatformMessage(ctx, "telegram", "private:1", "77")
	if err != nil || got.ID != "report" {
		t.Fatalf("actual target mapping=%#v/%v", got, err)
	}
	if _, err := store.Messages().FindByPlatformMessage(ctx, "telegram", "private:2", "77"); err == nil {
		t.Fatal("failed target was mapped")
	}
	if _, err := store.Messages().FindByPlatformMessage(ctx, "cli", "cron:task", "77"); err == nil {
		t.Fatal("task source was mapped as send target")
	}
}
