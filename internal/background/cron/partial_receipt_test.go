package cron

import (
	"context"
	"errors"
	"testing"

	"elbot/internal/delivery"
)

func TestFailedSendDoesNotMapPartialReceipt(t *testing.T) {
	store := newFakeCronRepo()
	wantErr := errors.New("later page failed")
	s := newTestCronService(t, Options{Store: store, EnabledPlatforms: []PlatformTarget{{Name: "qqonebot"}}, SendTarget: func(context.Context, delivery.Target, []delivery.Output) (receiptResult, error) {
		return sentReceipt(delivery.Receipt{PlatformMessageIDs: []string{"partial"}, SentMessages: []delivery.SentMessage{{Platform: "qqonebot", ChatType: "private", ChatID: "1001", PlatformMessageID: "partial"}}}), wantErr
	}})
	if _, err := deliverTestReport(t, s, "qqonebot", "task", "cron-message"); !errors.Is(err, wantErr) {
		t.Fatalf("send error=%v", err)
	}
	if len(store.mappings) != 0 {
		t.Fatalf("mappings=%#v", store.mappings)
	}
}
