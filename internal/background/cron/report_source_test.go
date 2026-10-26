package cron

import (
	"context"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/events"
)

func TestReportMappingUsesReceiptSourcesWithoutGuessing(t *testing.T) {
	store := newFakeCronRepo()
	sent := []delivery.SentMessage{
		{Platform: "qqofficial", ScopeID: "c2c:user", PlatformMessageID: "same"},
		{Platform: "telegram", ScopeID: "supergroup:-100", PlatformMessageID: "same"},
		{Platform: "telegram", ScopeID: "private:2", PlatformMessageID: "same"},
	}
	receipt := delivery.Receipt{PlatformMessageIDs: []string{"same"}, SentMessages: sent}
	svc := newTestCronService(t, Options{Store: store, SendTarget: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error) {
		return receipt, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
	}})
	if _, err := deliverTestReport(t, svc, "cli", "task", "report"); err != nil {
		t.Fatal(err)
	}
	if len(store.mappings) != len(sent) {
		t.Fatalf("mappings=%#v", store.mappings)
	}
	for i, message := range sent {
		got := store.mappings[i]
		if got.Platform != message.Platform || got.PlatformScopeID != message.ScopeID || got.PlatformMessageID != message.PlatformMessageID || got.SessionID != "task" || got.MessageID != "report" {
			t.Fatalf("mapping=%#v", got)
		}
	}
}
