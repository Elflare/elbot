package cron

import (
	"context"
	background "elbot/internal/background/share"
	"elbot/internal/events"
	"fmt"
	"testing"

	"elbot/internal/delivery"
)

func TestTextReportMapsPartialReceipt(t *testing.T) {
	repo := newFakeCronRepo()
	svc := newTestCronService(t, Options{Store: repo, SendTarget: func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
		if target.Platform != "telegram" || !target.Superadmins {
			t.Fatalf("target=%#v", target)
		}
		return delivery.Receipt{PlatformMessageIDs: []string{"77"}, SentMessages: []delivery.SentMessage{{Platform: "telegram", ScopeID: "private:1", PlatformMessageID: "77"}}}, events.LogResult{ResultStatus: events.ResultFailed}, fmt.Errorf("second target failed")
	}})
	_, err := deliverTestReport(t, svc, "telegram", "task", "report")
	if err == nil {
		t.Fatal("expected second target failure")
	}
	if len(repo.mappings) != 1 || repo.mappings[0] != (background.ReportMessageMap{Platform: "telegram", PlatformScopeID: "private:1", PlatformMessageID: "77", SessionID: "task", MessageID: "report"}) {
		t.Fatalf("mappings=%#v", repo.mappings)
	}
}
