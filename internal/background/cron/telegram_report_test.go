package cron

import (
	"context"
	background "elbot/internal/background/share"
	"fmt"
	"testing"

	"elbot/internal/delivery"
)

func TestTextReportMapsPartialReceipt(t *testing.T) {
	repo := newFakeCronRepo()
	svc := newTestCronService(t, Options{Store: repo, EnabledPlatforms: []PlatformTarget{{Name: "telegram"}}, SendTarget: func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
		if target.Platform != "telegram" || !target.Superadmins {
			t.Fatalf("target=%#v", target)
		}
		return delivery.Receipt{PlatformMessageIDs: []string{"77"}, SentMessages: []delivery.SentMessage{{Platform: "telegram", ChatType: "private", ChatID: "1", PlatformMessageID: "77"}}}, fmt.Errorf("second target failed")
	}})
	err := deliverTestReport(t, svc, "telegram", "task", "report")
	if err != nil {
		t.Fatalf("handled partial send failure escaped: %v", err)
	}
	if len(repo.mappings) != 1 || repo.mappings[0] != (background.ReportMessageMap{Platform: "telegram", ChatType: "private", ChatID: "1", PlatformMessageID: "77", SessionID: "task", MessageID: "report"}) {
		t.Fatalf("mappings=%#v", repo.mappings)
	}
}
