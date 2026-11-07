package cli

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	clishare "elbot/internal/platform/cli/share"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/signal"
)

func testUI(t *testing.T) (*UI, *signal.Signal[StatusChangedEvent, signal.Empty]) {
	t.Helper()
	status := signal.New[StatusChangedEvent, signal.Empty]("test.cli.status")
	binding := signal.New[BindingChangedEvent, signal.Empty]("test.cli.binding")
	u, err := ConnectUI(testPlatformName, testAgentEvents{status, binding})
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

func TestUIConnectFailureReleasesPartialSubscription(t *testing.T) {
	status := signal.New[StatusChangedEvent, signal.Empty]("test.status", signal.Options[StatusChangedEvent]{Single: true})
	defer status.Close(context.Background())
	binding := signal.New[BindingChangedEvent, signal.Empty]("test.binding")
	_ = binding.Close(context.Background())
	u, err := ConnectUI(testPlatformName, testAgentEvents{status, binding})
	if !errors.Is(err, signal.ErrClosed) || u == nil {
		t.Fatal(u, err)
	}
	if err := u.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A failed assembly must not retain the single available registration.
	c, err := status.Observe(func(context.Context, StatusChangedEvent) {}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close(context.Background())
}

func TestUIClosePreservesInFlightSendAndRetries(t *testing.T) {
	u, status := testUI(t)
	started, release := make(chan context.Context, 1), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	u.display.send = func(ctx context.Context, _ clishare.DisplayTarget, _ runtimestatus.Snapshot) error {
		started <- ctx
		<-release
		return ctx.Err()
	}
	ctx := contextinfo.WithConversation(context.Background(), contextinfo.Conversation{PlatformData: testDisplayTarget{id: "cli:test"}})
	if _, err := status.Emit(ctx, StatusChangedEvent{Conversation: testConversation(ctx), Snapshot: runtimestatus.Snapshot{SessionID: "s", Phase: runtimestatus.PhaseLLM}, Version: 1}); err != nil {
		t.Fatal(err)
	}
	var sending context.Context
	select {
	case sending = <-started:
	case <-time.After(time.Second):
		t.Fatal("display never started")
	}
	u.BeginClose()
	if sending.Err() != nil {
		t.Error("UI close canceled shared transport", sending.Err())
	}
	deadline, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := u.Close(deadline); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if u.display.stopped() {
		t.Fatal("reported exit while callback still active")
	}
	once.Do(func() { close(release) })
	if err := u.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !u.display.stopped() {
		t.Fatal("retry returned before actual exit")
	}
}
