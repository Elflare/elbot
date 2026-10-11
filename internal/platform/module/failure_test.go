package module

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"elbot/internal/events"
	"elbot/internal/platform"
	"elbot/internal/platform/builtin"
	"elbot/internal/signal"
)

type failingPlatformRuntime struct {
	fakeRuntimeWithCaller
	release <-chan struct{}
	err     error
}

func (r failingPlatformRuntime) Run(ctx context.Context, _ platform.PlatformHandler) error {
	select {
	case <-r.release:
		return r.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// A service mode loop cannot acquire application ownership from this marker.
func (failingPlatformRuntime) StopAppOnExit() bool { return true }

func TestServicePlatformFailureIsolatedAndNotifiesOtherSuperadmins(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	m, err := NewModule(ctx, ModuleDeps{Mode: builtin.ModeService, Superadmins: map[string][]string{
		"cli": {"failed-owner"}, "healthy": {"admin", "admin", " "}, "offline": {"offline-owner"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	notices := make(chan events.Notification, 4)
	s, err := m.notifications.AddPlatform("healthy")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Connect(func(_ context.Context, n events.Notification) (events.NotificationReceipt, error) {
		notices <- n
		return events.NotificationReceipt{}, nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	logs := make(chan events.LogRecord, 4)
	c, err := events.LogSubmitted.Observe(func(_ context.Context, r events.LogRecord) error {
		if r.EventName == "platform_stopped_with_error" {
			logs <- r
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Disconnect()
	release := make(chan struct{})
	peer := &runtimeWithShutdownGate{fakeRuntimeWithCaller: fakeRuntimeWithCaller{name: "healthy"}, started: make(chan struct{})}
	m.runtimes = []platform.Runtime{peer, failingPlatformRuntime{fakeRuntimeWithCaller{"cli"}, release, errors.New("listener failed")}}
	stop := make(chan struct{}, 1)
	if err := m.Start(nil, func() { stop <- struct{}{} }); err != nil {
		t.Fatal(err)
	}
	<-peer.started
	close(release)
	select {
	case n := <-notices:
		if n.Target.Platform != "healthy" || n.Target.ID != "admin" || !strings.Contains(n.Text, "listener failed") {
			t.Fatal("wrong failure notification", n)
		}
	case <-ctx.Done():
		t.Fatal("failure notification missing")
	}
	select {
	case r := <-logs:
		if !strings.Contains(r.Content, "platform=cli") || !strings.Contains(r.Content, "listener failed") {
			t.Fatal(r)
		}
	case <-ctx.Done():
		t.Fatal("failure log missing")
	}
	select {
	case <-stop:
		t.Fatal("service platform stopped application")
	default:
	}
	select {
	case <-m.Done():
		t.Fatal("healthy platform was stopped")
	default:
	}
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if len(notices) != 0 || len(logs) != 0 {
		t.Fatal("duplicate notification or normal shutdown reported as failure")
	}
}

func TestServiceRemainsAliveWithoutRunningPlatforms(t *testing.T) {
	for _, withFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "all_failed"}[withFailure], func(t *testing.T) {
			m, err := NewModule(t.Context(), ModuleDeps{Mode: builtin.ModeService})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Close(context.Background()) })
			if withFailure {
				release := make(chan struct{})
				close(release)
				m.runtimes = []platform.Runtime{failingPlatformRuntime{fakeRuntimeWithCaller{"cli"}, release, errors.New("startup failed")}}
			}
			if err := m.Start(nil, nil); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
			defer cancel()
			if err := m.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("service exited: %v", err)
			}
			if err := m.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case <-m.Done():
			default:
				t.Fatal("service did not stop")
			}
		})
	}
}
