package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/signal"
)

func TestRemoteNotificationFailureLoggedByCLI(t *testing.T) {
	server := &RemoteServer{name: testPlatformName, clients: map[string]map[*remoteClientConn]struct{}{}}
	source := signal.New[events.Notification, signal.Empty]("test.remote.notice")
	defer source.Close(context.Background())
	c, err := server.ConnectNotifications(source)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Disconnect()
	logs := make(chan events.LogRecord, 4)
	l, err := events.LogSubmitted.Observe(func(_ context.Context, r events.LogRecord) error {
		if r.Module == "cli" {
			logs <- r
		}
		return nil
	}, signal.ConnectOptions{Shutdown: signal.Drain})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Disconnect()
	ctx := contextinfo.WithExecution(t.Context(), contextinfo.Execution{RequestID: "request"})
	_, err = source.Emit(ctx, events.Notification{Text: "another platform failed", Target: events.NotificationTarget{
		Platform: "cli", ChatType: contextinfo.ChatPrivate, ID: "offline-admin",
	}})
	if err != nil {
		t.Fatal("handled notification failure escaped", err)
	}
	select {
	case record := <-logs:
		if record.EventName != "notification_rejected" || record.ResultStatus != events.ResultFailed || !strings.Contains(record.Content, "no matching cli clients") || !strings.Contains(record.Content, "offline-admin") || record.Identity.RequestID != "request" {
			t.Fatal(record)
		}
	case <-time.After(time.Second):
		t.Fatal("missing CLI notification failure record")
	}
}
