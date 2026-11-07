package cli

import (
	"context"
	globalevents "elbot/internal/events"
	"elbot/internal/signal"
	"os"
	"testing"

	"elbot/internal/delivery"
)

func TestSendToAcceptsCLITarget(t *testing.T) {
	adapter := New(testPlatformName, "user")
	if _, err := adapter.SendTo(context.Background(), delivery.Target{Platform: "cli", Superadmins: true}, []delivery.Output{delivery.ImagePath("pic.png")}); err != nil {
		t.Fatalf("SendTo: %v", err)
	}
}

func TestSendToRejectsOtherPlatform(t *testing.T) {
	adapter := New(testPlatformName, "user")
	if _, err := adapter.SendTo(context.Background(), delivery.Target{Platform: "qqonebot"}, []delivery.Output{delivery.Text("hello")}); err == nil {
		t.Fatal("expected platform mismatch error")
	}
}

func TestRunPublishesGlobalPlatformConnection(t *testing.T) {
	calls := 0
	connection, err := globalevents.PlatformConnected.Observe(func(_ context.Context, e globalevents.PlatformConnectedEvent) {
		calls++
		if e.Platform != "cli" {
			t.Errorf("platform=%q", e.Platform)
		}

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
	adapter := New(testPlatformName, "user")
	defer adapter.Close(context.Background())
	if err := adapter.Start(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
}
