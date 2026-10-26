package cron

import (
	"context"
	"elbot/internal/events"
	"errors"
	"testing"

	"elbot/internal/delivery"
)

func TestPartialSendMapsSuccessAndReturnsFailure(t *testing.T) {
	store := newFakeCronRepo()
	wantErr := errors.New("later page failed")
	s := newTestCronService(t, Options{Store: store, SendTarget: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error) {
		{
			logResult := events.LogResult{ResultStatus: events.ResultSucceeded}
			if wantErr != nil {
				logResult.ResultStatus = events.ResultFailed
				logResult.Content = wantErr.Error()
				if wantErr == context.Canceled {
					logResult.ResultStatus = events.ResultCanceled
				}
			}
			return delivery.Receipt{PlatformMessageIDs: []string{"partial"}, SentMessages: []delivery.SentMessage{{Platform: "qqonebot", ScopeID: "private:1001", PlatformMessageID: "partial"}}}, logResult, wantErr
		}
	}})
	if _, err := deliverTestReport(t, s, "qqonebot", "task", "cron-message"); !errors.Is(err, wantErr) {
		t.Fatalf("send error=%v", err)
	}
	if len(store.mappings) != 1 || store.mappings[0] != (ReportMessageMap{Platform: "qqonebot", PlatformScopeID: "private:1001", PlatformMessageID: "partial", SessionID: "task", MessageID: "cron-message"}) {
		t.Fatalf("mappings=%#v", store.mappings)
	}
}
