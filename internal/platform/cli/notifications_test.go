package cli

import (
	"context"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/signal"
)

func TestRemoteNotificationFailureLoggedOnceByDelivery(t *testing.T) {
	server := &RemoteServer{clients: map[string]map[*remoteClientConn]struct{}{}}
	source := signal.New[events.Notification, events.NotificationReceipt]("test.remote.notice")
	defer source.Close(context.Background())
	c, err := server.ConnectNotifications(source)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Disconnect()
	logs := make(chan events.LogRecord, 4)
	l, err := events.LogSubmitted.Observe(func(_ context.Context, r events.LogRecord) error {
		if r.Module == "delivery" {
			logs <- r
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Disconnect()
	_, err = source.Emit(t.Context(), events.Notification{Text: "another platform failed", Target: events.NotificationTarget{
		Platform: "cli", Kind: contextinfo.ConversationPrivate, ID: "offline-admin",
	}})
	if err == nil {
		t.Fatal("offline recipient unexpectedly succeeded")
	}
	if len(logs) != 1 {
		t.Fatal("expected one Delivery failure record", len(logs))
	}
	record := <-logs
	if record.EventName != "chat_output_failed" || record.ResultStatus != events.ResultFailed || !strings.Contains(record.Content, "no matching cli clients") {
		t.Fatal(record)
	}
}
