package cli

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/signal"
)

func testUI(t *testing.T) (*UI, *signal.Signal[StatusChangedEvent, signal.Empty]) {
	t.Helper()
	status := signal.New[StatusChangedEvent, signal.Empty]("test.cli.status")
	binding := signal.New[BindingChangedEvent, signal.Empty]("test.cli.binding")
	u, err := ConnectUI(testAgentEvents{status, binding})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := u.Close(context.Background()); err != nil {
			t.Error(err)
		}
		_ = status.Close(context.Background())
		_ = binding.Close(context.Background())
	})
	return u, status
}

func captureCLIOutput(t *testing.T, run func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	previous := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = previous; _ = w.Close() }()
	run()
	_ = w.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestReasoningEndsOnCancellationAndDoesNotReplayBackground(t *testing.T) {
	u, _ := testUI(t)
	background := contextinfo.WithConversation(context.Background(), contextinfo.Conversation{Source: contextinfo.Source{Platform: "cli"}})
	foreground, cancel := context.WithCancel(contextinfo.WithConversation(context.Background(), contextinfo.Conversation{PlatformData: messageData{adapter: New()}}))
	defer cancel()
	got := captureCLIOutput(t, func() {
		stream := u.StartReasoning(background)
		stream.Append(background, "hidden")
		stream.Append(foreground, "")
		stream.Append(foreground, "first")
		stream.Append(foreground, "second")
		cancel()
		stream.Append(foreground, "canceled")
		stream.Finish(foreground)
		stream.Finish(foreground)
		stream.Append(context.WithoutCancel(foreground), "late")
	})
	if got != "[thinking] first\nsecond\n[/thinking]\n\n" {
		t.Fatalf("reasoning boundaries/order: %q", got)
	}
}

func TestReasoningWithoutDeltasOrAfterUIShutdownIsSilent(t *testing.T) {
	u, _ := testUI(t)
	ctx := contextinfo.WithConversation(context.Background(), contextinfo.Conversation{PlatformData: messageData{adapter: New()}})
	got := captureCLIOutput(t, func() {
		u.StartReasoning(ctx).Finish(ctx)
		stream := u.StartReasoning(ctx)
		if err := u.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		stream.Append(ctx, "after close")
		stream.Finish(ctx)
	})
	if got != "" {
		t.Fatalf("unexpected display: %q", got)
	}
}

func TestReasoningDestinationChangeClosesOriginalView(t *testing.T) {
	u, _ := testUI(t)
	one := contextinfo.WithConversation(context.Background(), contextinfo.Conversation{PlatformData: messageData{adapter: New()}})
	two := contextinfo.WithConversation(context.Background(), contextinfo.Conversation{PlatformData: messageData{adapter: New()}})
	got := captureCLIOutput(t, func() {
		stream := u.StartReasoning(one)
		stream.Append(one, "one")
		stream.Append(two, "two")
		stream.Finish(two)
	})
	if strings.Count(got, "[thinking]") != 2 || strings.Count(got, "[/thinking]") != 2 || strings.Index(got, "[/thinking]") > strings.Index(got, "two") {
		t.Fatal(got)
	}
}
