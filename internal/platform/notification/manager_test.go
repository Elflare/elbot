// manager_test.go 验证随组件迁入的既有行为。

package notification

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/storage/sqlite"
)

func testManager(t *testing.T, primary string) *Manager {
	t.Helper()
	m, err := New(primary)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return m
}
func addTestPlatform(t *testing.T, m *Manager, name string, h signal.Handler[events.Notification, events.NotificationReceipt]) *signal.Signal[events.Notification, events.NotificationReceipt] {
	t.Helper()
	s, err := m.AddPlatform(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Connect(h, signal.ConnectOptions{}); err != nil {
		t.Fatal(err)
	}
	return s
}
func TestOriginalSourceAndPartialFailure(t *testing.T) {
	m := testManager(t, "test")
	wantErr := errors.New("partially sent")
	origin := contextinfo.Conversation{Source: contextinfo.Source{Platform: "test", ConversationKind: contextinfo.ConversationPrivate, ConversationID: "one", ScopeID: "private:one"}, PlatformMessageID: "original", PlatformData: "first-connection"}
	addTestPlatform(t, m, "test", func(ctx context.Context, n events.Notification) (events.NotificationReceipt, error) {
		info, _ := contextinfo.ConversationFromContext(ctx)
		if info != origin || n.Level != slog.LevelWarn {
			t.Error("lost captured origin or severity", info, n)
		}
		return events.NotificationReceipt{PlatformMessageIDs: []string{"sent"}}, wantErr
	})
	n := events.NewNotification(contextinfo.WithConversation(context.Background(), origin), slog.LevelWarn, "warning")
	worker := contextinfo.WithConversation(context.Background(), contextinfo.Conversation{Source: origin.Source, PlatformData: "other-connection"})
	r, err := events.NotificationRequested.Emit(worker, n)
	if !errors.Is(err, wantErr) || len(r) != 1 || len(r[0].Value.PlatformMessageIDs) != 1 || !r[0].Started {
		t.Fatal(r, err)
	}
}
func TestPlatformFullWarnsAndOtherPlatformStillSends(t *testing.T) {
	m := testManager(t, "slow")
	started := make(chan struct{}, 8)
	release := make(chan struct{})
	defer close(release)
	slow := addTestPlatform(t, m, "slow", func(context.Context, events.Notification) (events.NotificationReceipt, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return events.NotificationReceipt{}, nil
	})
	addTestPlatform(t, m, "fast", func(context.Context, events.Notification) (events.NotificationReceipt, error) {
		return events.NotificationReceipt{PlatformMessageIDs: []string{"fast"}}, nil
	})
	for i := range 8 {
		if _, err := slow.EmitAsync(context.Background(), events.Notification{Target: events.NotificationTarget{ID: string(rune('a' + i))}, Text: "busy"}); err != nil {
			t.Fatal(err)
		}
	}
	for range 8 {
		<-started
	}
	for range 256 {
		if _, err := slow.EmitAsync(context.Background(), events.Notification{Text: "queued"}); err != nil {
			t.Fatal(err)
		}
	}
	logs := make(chan events.LogRecord, 1)
	c, err := events.LogSubmitted.Observe(func(_ context.Context, r events.LogRecord) error {
		if r.EventName == "notification_queue_full" {
			logs <- r
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	n := events.Notification{Target: events.NotificationTarget{Platform: "slow", Kind: contextinfo.ConversationPrivate, ID: "one"}, Text: "overflow"}
	if _, err := events.NotificationRequested.Emit(context.Background(), n); !errors.Is(err, signal.ErrQueueFull) {
		t.Fatal(err)
	}
	select {
	case r := <-logs:
		if r.Level != slog.LevelWarn {
			t.Fatal(r)
		}
	case <-time.After(time.Second):
		t.Fatal("missing overflow warning")
	}
	n.Target.Platform = "fast"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if r, err := events.NotificationRequested.Emit(ctx, n); err != nil || r[0].Value.PlatformMessageIDs[0] != "fast" {
		t.Fatal(r, err)
	}
}
func TestQueuedNotificationRejectsExpiredBinding(t *testing.T) {
	m := testManager(t, "cli")
	started, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var calls atomic.Int32
	s := addTestPlatform(t, m, "cli", func(_ context.Context, n events.Notification) (events.NotificationReceipt, error) {
		calls.Add(1)
		if n.Text == "block" {
			close(started)
			<-release
		}
		return events.NotificationReceipt{}, nil
	})
	store, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sessions := session.NewService(store)
	scope := session.Scope{ActorID: "cli:one", Platform: "cli", PlatformScopeID: "local", IsCLI: true}
	if _, err := sessions.Create(context.Background(), scope, session.CreateRequest{}); err != nil {
		t.Fatal(err)
	}
	_, binding, err := sessions.CurrentBound(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	n := events.Notification{Target: events.NotificationTarget{Platform: "cli"}, Text: "block"}
	_, _ = s.EmitAsync(context.Background(), n)
	<-started
	n.Text = "old"
	ctx := session.WithBinding(context.Background(), binding)
	e, err := s.EmitAsync(ctx, n)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Create(context.Background(), scope, session.CreateRequest{}); err != nil {
		t.Fatal(err)
	}
	close(release)
	deadline, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if results, err := e.Wait(deadline); !errors.Is(err, context.Canceled) || len(results) != 1 || results[0].Started {
		t.Fatal(results, err)
	}
	if calls.Load() != 1 {
		t.Fatal("queued notification bypassed the active key")
	}
}
func TestManagerOwnershipAndMissingPlatform(t *testing.T) {
	m := testManager(t, "cli")
	if _, err := New("cli"); err == nil {
		t.Fatal("duplicate global manager")
	}
	if _, err := events.NotificationRequested.Emit(context.Background(), events.Notification{Text: "missing"}); err == nil {
		t.Fatal("missing platform succeeded")
	}
	s, err := m.AddPlatform("cli")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := events.NotificationRequested.Emit(context.Background(), events.Notification{Text: "no receiver"}); !errors.Is(err, signal.ErrNoReceiver) {
		t.Fatal(err)
	}
	if _, err := s.Connect(func(context.Context, events.Notification) (events.NotificationReceipt, error) {
		return events.NotificationReceipt{}, nil
	}, signal.ConnectOptions{}); err != nil {
		t.Fatal(err)
	}
}
