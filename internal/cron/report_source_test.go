package cron

import (
	"context"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/storage"
)

func TestReportMappingUsesReceiptSourcesWithoutGuessing(t *testing.T) {
	ctx := context.Background()
	store := newCronSQLiteStore(t)
	row := &storage.Session{ID: "task", OwnerID: "cli:local", Platform: "cli", PlatformScopeID: "cron:task", Mode: storage.SessionModeBackground, Status: storage.SessionStatusActive}
	if err := store.Sessions().Create(ctx, row); err != nil {
		t.Fatal(err)
	}
	if err := store.Messages().Append(ctx, &storage.Message{ID: "report", SessionID: row.ID, Role: storage.RoleAssistant, Content: "report"}); err != nil {
		t.Fatal(err)
	}
	sent := []delivery.SentMessage{
		{Platform: "qqofficial", ScopeID: "c2c:user", PlatformMessageID: "same"},
		{Platform: "telegram", ScopeID: "supergroup:-100", PlatformMessageID: "same"},
		{Platform: "telegram", ScopeID: "private:2", PlatformMessageID: "same"},
	}
	receipt := delivery.Receipt{PlatformMessageIDs: []string{"same"}, SentMessages: sent}
	svc := newTestCronService(t, Options{Store: store, SendTarget: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error) {
		return receipt, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
	}})
	if _, err := deliverTestReport(t, svc, "cli", row.ID, "report"); err != nil {
		t.Fatal(err)
	}
	for _, message := range sent {
		mapped, err := store.Messages().FindByPlatformMessage(ctx, message.Platform, message.ScopeID, message.PlatformMessageID)
		if err != nil || mapped.ID != "report" {
			t.Fatalf("mapping %#v: %#v/%v", message, mapped, err)
		}
	}
	if _, err := store.Messages().FindByPlatformMessage(ctx, "cli", "cron:task", "same"); err == nil {
		t.Fatal("invented source mapping")
	}
}
