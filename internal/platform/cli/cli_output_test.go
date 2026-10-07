package cli

import (
	"context"
	globalevents "elbot/internal/events"
	"elbot/internal/signal"
	"log/slog"
	"os"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"elbot/internal/delivery"
)

func TestSendNoticeAcceptsCLITarget(t *testing.T) {
	adapter := New()
	if _, err := adapter.SendNotice(context.Background(), delivery.Notice{Target: delivery.Target{Platform: "cli", Superadmins: true}, Outputs: []delivery.Output{delivery.ImagePath("pic.png")}}); err != nil {
		t.Fatalf("SendNotice: %v", err)
	}
}

func TestSendNoticeRejectsOtherPlatform(t *testing.T) {
	adapter := New()
	if _, err := adapter.SendNotice(context.Background(), delivery.Notice{Target: delivery.Target{Platform: "qqonebot"}, Outputs: []delivery.Output{delivery.Text("hello")}}); err == nil {
		t.Fatal("expected platform mismatch error")
	}
}

func TestRemoteNoticeMessageCarriesLevel(t *testing.T) {
	msg := remoteNoticeMessage(delivery.Notice{Outputs: []delivery.Output{delivery.Text("careful")}, Level: slog.LevelWarn})
	if msg.Type != remoteMsgNotice || msg.Level != "WARN" || msg.Text != "careful" {
		t.Fatalf("remote notice = %#v", msg)
	}
}

func TestRemoteClientNoticeLevelDefaultsToInfo(t *testing.T) {
	client := &RemoteClient{output: make(chan tea.Msg, 2)}
	for _, test := range []struct {
		level string
		want  slog.Level
	}{
		{level: "WARN", want: slog.LevelWarn},
		{level: "", want: slog.LevelInfo},
	} {
		client.handleServerMessage(remoteMessage{Type: remoteMsgNotice, Text: "notice", Level: test.level})
		got := (<-client.output).(tuiNoticeMsg)
		if got.Level != test.want {
			t.Fatalf("level %q parsed as %s, want %s", test.level, got.Level, test.want)
		}
	}
}

func TestRunPublishesGlobalPlatformConnection(t *testing.T) {
	calls := 0
	connection, err := globalevents.PlatformConnected.Connect(func(_ context.Context, e globalevents.PlatformConnectedEvent) error {
		calls++
		if e.Platform != "cli" {
			t.Errorf("platform=%q", e.Platform)
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Disconnect()
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if _, err = writer.WriteString("/exit\n"); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	original := os.Stdin
	os.Stdin = input
	defer func() { os.Stdin = original }()
	if err := New().Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
}
