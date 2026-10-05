package cron

import (
	"context"
	"testing"

	"elbot/internal/delivery"
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
	svc := NewService(Options{Store: store})
	sent := []delivery.SentMessage{
		{Platform: "qqofficial", ScopeID: "c2c:user", PlatformMessageID: "same"},
		{Platform: "telegram", ScopeID: "supergroup:-100", PlatformMessageID: "same"},
		{Platform: "telegram", ScopeID: "private:2", PlatformMessageID: "same"},
	}
	receipt := delivery.Receipt{PlatformMessageIDs: []string{"legacy-only", "same"}, SentMessages: append(append([]delivery.SentMessage(nil), sent...), delivery.SentMessage{Platform: "qqofficial", PlatformMessageID: "incomplete"})}
	svc.mapReportReceipt(ctx, "task", row.ID, "report", receipt)
	for _, message := range sent {
		mapped, err := store.Messages().FindByPlatformMessage(ctx, message.Platform, message.ScopeID, message.PlatformMessageID)
		if err != nil || mapped.ID != "report" {
			t.Fatalf("mapping %#v: %#v/%v", message, mapped, err)
		}
	}
	for _, id := range []string{"legacy-only", "incomplete"} {
		for _, scope := range []string{"", "cron:task", "c2c:user"} {
			if _, err := store.Messages().FindByPlatformMessage(ctx, "qqofficial", scope, id); err == nil {
				t.Fatalf("invented mapping: %s/%s", scope, id)
			}
		}
	}
}
