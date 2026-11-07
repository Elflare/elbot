package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/platform/share"
	"elbot/internal/signal"
)

func TestRemoteNotificationFailureLoggedByCLI(t *testing.T) {
	adapter, err := NewRemoteServer(RemoteServerOptions{
		PlatformName: testPlatformName,
		Config:       Config{Server: ServerConfig{Enabled: true, Listen: "127.0.0.1:0", Tokens: map[string][]string{"user": {"TOKEN"}}}},
		ConfigEnv:    func(string) (string, bool, error) { return "", false, nil },
		Superadmins:  share.NewSuperadmins(testPlatformName, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	source := signal.New[events.Notification, signal.Empty]("test.remote.notice")
	defer source.Close(context.Background())
	c, err := adapter.ConnectNotifications(source)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Disconnect()
	logs := make(chan events.LogRecord, 4)
	l, err := events.LogSubmitted.Observe(func(_ context.Context, r events.LogRecord) {
		if r.Module == "cli" {
			logs <- r
		}

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
		if record.EventName != "notification_send_failed" || record.ResultStatus != events.ResultFailed || !strings.Contains(record.Content, "no matching cli clients") || !strings.Contains(record.Content, "offline-admin") || record.Identity.RequestID != "request" {
			t.Fatal(record)
		}
	case <-time.After(time.Second):
		t.Fatal("missing CLI notification failure record")
	}
}
