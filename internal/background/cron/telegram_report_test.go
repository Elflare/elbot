package cron

import (
	"context"
	background "elbot/internal/background/share"
	"elbot/internal/events"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/platform/dispatch"
	"elbot/internal/platform/telegram"
)

func TestTextReportMapsRealTelegramPartialReceipt(t *testing.T) {
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
	adapter := telegram.New(telegram.Config{BotToken: "token", APIBaseURL: server.URL, Format: "plain", Superadmins: []string{"1", "2"}}, nil, nil)
	store := newCronSQLiteStore(t)
	router := dispatch.New(dispatch.Options{Primary: adapter, Store: store})
	repo := newFakeCronRepo()
	svc := newTestCronService(t, Options{Store: repo, SendTarget: func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
		return router.SendTo(ctx, target, outputs)
	}})
	_, err := deliverTestReport(t, svc, "telegram", "task", "report")
	if err == nil {
		t.Fatal("expected second target failure")
	}
	if len(repo.mappings) != 1 || repo.mappings[0] != (background.ReportMessageMap{Platform: "telegram", PlatformScopeID: "private:1", PlatformMessageID: "77", SessionID: "task", MessageID: "report"}) {
		t.Fatalf("mappings=%#v", repo.mappings)
	}
}
