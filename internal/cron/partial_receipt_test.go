package cron

import (
	"context"
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
	s := NewService(Options{Store: store, SendTarget: func(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error) {
		return delivery.Receipt{PlatformMessageIDs: []string{"partial"}}, wantErr
	}})
	if err := s.sendOutputsToPlatformTarget(ctx, "partial", "qqonebot", delivery.Target{PrivateUserID: "1001"}, []delivery.Output{delivery.Text("report")}, row.ID, "cron-message", "private:1001"); !errors.Is(err, wantErr) {
		t.Fatalf("send error=%v", err)
	}
	msg, err := store.Messages().FindByPlatformMessage(ctx, "qqonebot", "private:1001", "partial")
	if err != nil || msg.ID != "cron-message" {
		t.Fatalf("mapping=%#v/%v", msg, err)
	}
}
