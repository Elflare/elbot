package cron

import (
	"context"
	"elbot/internal/events"
	"errors"
	"testing"

	"elbot/internal/delivery"
	"elbot/internal/storage"
)

func TestPartialSendMapsSuccessAndReturnsFailure(t *testing.T) {
	ctx := context.Background()
	store := newCronSQLiteStore(t)
	row := &storage.Session{ID: "cron-session", OwnerID: "cli:local", Platform: "cli", PlatformScopeID: "cron:partial", Mode: storage.SessionModeWork, Status: storage.SessionStatusActive}
	if err := store.Sessions().Create(ctx, row); err != nil {
		t.Fatal(err)
	}
	if err := store.Messages().Append(ctx, &storage.Message{ID: "cron-message", SessionID: row.ID, Role: storage.RoleAssistant, Content: "report"}); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("later page failed")
	s := NewService(Options{Store: store, SendTarget: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error) {
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
	if _, err := s.sendOutputsToPlatformTarget(ctx, "partial", "qqonebot", delivery.Target{PrivateUserID: "1001"}, []delivery.Output{delivery.Text("report")}, row.ID, "cron-message"); !errors.Is(err, wantErr) {
		t.Fatalf("send error=%v", err)
	}
	msg, err := store.Messages().FindByPlatformMessage(ctx, "qqonebot", "private:1001", "partial")
	if err != nil || msg.ID != "cron-message" {
		t.Fatalf("mapping=%#v/%v", msg, err)
	}
}
