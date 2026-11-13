package cron

import (
	"context"
	"fmt"
	"testing"

	"elbot/internal/delivery"
)

func TestTextReportDiscardsPartialReceiptOnFailure(t *testing.T) {
	repo := newFakeCronRepo()
	svc := newTestCronService(t, Options{Store: repo, EnabledPlatforms: []PlatformTarget{{Name: "telegram"}}, SendTarget: func(ctx context.Context, target delivery.Target, outputs []delivery.Output) (receiptResult, error) {
		if target.Platform != "telegram" || !target.Superadmins {
			t.Fatalf("target=%#v", target)
		}
		return receiptResult{}, fmt.Errorf("second target failed")
	}})
	if _, err := deliverTestReport(t, svc, "telegram", "task", "report"); err == nil {
		t.Fatal("send failure was swallowed")
	}
	if len(repo.mappings) != 0 {
		t.Fatalf("mappings=%#v", repo.mappings)
	}
}
