package cli

import (
	"context"
	"log/slog"
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
