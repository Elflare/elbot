package cron

import (
	"context"
	background "elbot/internal/background/share"
	"errors"
	"testing"

	"elbot/internal/delivery"
)

func TestPartialSendMapsSuccessAndHandlesFailure(t *testing.T) {
	store := newFakeCronRepo()
	wantErr := errors.New("later page failed")
	s := newTestCronService(t, Options{Store: store, EnabledPlatforms: []PlatformTarget{{Name: "qqonebot"}}, SendTarget: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error) {
		return delivery.Receipt{PlatformMessageIDs: []string{"partial"}, SentMessages: []delivery.SentMessage{{Platform: "qqonebot", ChatType: "private", ChatID: "1001", PlatformMessageID: "partial"}}}, wantErr
	}})
	if err := deliverTestReport(t, s, "qqonebot", "task", "cron-message"); err != nil {
		t.Fatalf("send error=%v", err)
	}
	if len(store.mappings) != 1 || store.mappings[0] != (background.ReportMessageMap{Platform: "qqonebot", ChatType: "private", ChatID: "1001", PlatformMessageID: "partial", SessionID: "task", MessageID: "cron-message"}) {
		t.Fatalf("mappings=%#v", store.mappings)
	}
}
