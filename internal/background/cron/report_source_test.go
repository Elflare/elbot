package cron

import (
	"context"
	"testing"

	"elbot/internal/delivery"
)

func TestReportMappingUsesReceiptSourcesWithoutGuessing(t *testing.T) {
	store := newFakeCronRepo()
	sent := []delivery.SentMessage{
		{Platform: "qqofficial", ChatType: "private", ChatID: "user", PlatformMessageID: "same"},
		{Platform: "telegram", ChatType: "group", ChatID: "-100", PlatformMessageID: "same"},
		{Platform: "telegram", ChatType: "private", ChatID: "2", PlatformMessageID: "same"},
	}
	receipt := delivery.Receipt{PlatformMessageIDs: []string{"same"}, SentMessages: sent}
	svc := newTestCronService(t, Options{Store: store, SendTarget: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error) {
		return receipt, nil
	}})
	if err := deliverTestReport(t, svc, "cli", "task", "report"); err != nil {
		t.Fatal(err)
	}
	if len(store.mappings) != len(sent) {
		t.Fatalf("mappings=%#v", store.mappings)
	}
	for i, message := range sent {
		got := store.mappings[i]
		if got.Platform != message.Platform || got.ChatID != message.ChatID || got.PlatformMessageID != message.PlatformMessageID || got.SessionID != "task" || got.MessageID != "report" {
			t.Fatalf("mapping=%#v", got)
		}
	}
}
