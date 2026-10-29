// manager_test.go 验证通知路由、故障异步投递及队列关闭行为。

package notification

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/session"
	"elbot/internal/signal"
	"elbot/internal/storage/sqlite"
)

func TestPlatformFailureDeliveryIsAsyncAndIsolated(t *testing.T) {
	m := testManager(t, "slow")
	release := make(chan struct{})
	defer close(release)
	started := make(chan string, 8)
	var slowCalls, fastCalls atomic.Int32
	for _, name := range []string{"slow", "fast"} {
		addTestPlatform(t, m, name, func(_ context.Context, n events.Notification) (events.NotificationReceipt, error) {
			if n.Text == "barrier" {
				return events.NotificationReceipt{}, nil
			}
			if n.Level != slog.LevelError || n.Text != "平台 failed 异常退出：offline" || n.Target.Platform != name || n.Target.ID != "admin" || n.Target.Kind != contextinfo.ConversationPrivate || n.Origin != nil {
				t.Errorf("unexpected failure notification: %+v", n)
			}
			if name == "slow" {
				slowCalls.Add(1)
				started <- name
				<-release
				return events.NotificationReceipt{}, errors.New("send failed")
			}
			fastCalls.Add(1)
			started <- name
			return events.NotificationReceipt{}, nil
		})
	}
	targets := []events.NotificationTarget{
		{Platform: "slow", Kind: contextinfo.ConversationPrivate, ID: "admin"},
		{Platform: "fast", Kind: contextinfo.ConversationPrivate, ID: "admin"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ctx = contextinfo.WithBackgroundKind(ctx, contextinfo.BackgroundKindCron)
	returned := make(chan struct{})
	go func() {
		m.NotifyPlatformFailure(ctx, "failed", errors.New("offline"), targets)
		close(returned)
	}()
	select {
	case <-returned:
	case <-ctx.Done():
		t.Fatal("failure notification waited for sender")
	}
	for range targets {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("slow platform blocked another destination")
		}
	}
	m.NotifyPlatformFailure(ctx, "failed", errors.New("offline"), nil)
	canceled, stop := context.WithCancel(ctx)
	stop()
	m.NotifyPlatformFailure(canceled, "failed", errors.New("offline"), targets)
	release <- struct{}{}
	// 同目标的同步标记等待此前通知完成，避免用睡眠猜测队列进度。
	for _, target := range targets {
		if _, err := events.NotificationRequested.Emit(ctx, events.Notification{Target: target, Text: "barrier"}); err != nil {
			t.Fatal(err)
		}
	}
	if slowCalls.Load() != 1 || fastCalls.Load() != 1 {
		t.Fatalf("unexpected additional delivery: slow=%d fast=%d", slowCalls.Load(), fastCalls.Load())
	}
}
func TestManagerCloseWaitsForSenderAndCanRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := testManager(t, "remote")
		release := make(chan struct{})
		defer close(release)
		var calls atomic.Int32
		var sendContext context.Context
		s := addTestPlatform(t, m, "remote", func(ctx context.Context, _ events.Notification) (events.NotificationReceipt, error) {
			calls.Add(1)
			sendContext = ctx
			<-release
			return events.NotificationReceipt{}, nil
		})
		targets := []events.NotificationTarget{{Platform: "remote", Kind: contextinfo.ConversationPrivate, ID: "admin"}}
		m.NotifyPlatformFailure(context.Background(), "failed", errors.New("offline"), targets)
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatal("sender did not start")
		}
		m.NotifyPlatformFailure(context.Background(), "failed", errors.New("queued"), targets)
		synctest.Wait()
		m.BeginClose()
		synctest.Wait()
		if !errors.Is(sendContext.Err(), context.Canceled) {
			t.Fatalf("sender was not canceled: %v", sendContext.Err())
		}
		if _, err := m.AddPlatform("late"); !errors.Is(err, signal.ErrClosed) {
			t.Fatalf("registration after close: %v", err)
		}
		m.NotifyPlatformFailure(context.Background(), "failed", errors.New("late"), targets)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := m.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Close did not wait for actual sender exit: %v", err)
		}
		select {
		case <-s.Done():
			t.Fatal("queue reported completion while sender was still running")
		default:
		}
		release <- struct{}{}
		if err := m.Close(context.Background()); err != nil {
			t.Fatalf("retry Close: %v", err)
		}
		if calls.Load() != 1 {
			t.Fatalf("queued or late notification started: %d", calls.Load())
		}
		select {
		case <-s.Done():
		default:
			t.Fatal("queue not completed after sender exit")
		}
	})
}

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

func TestBackgroundDefaultNotificationIsSilentAndExplicitTargetDelivers(t *testing.T) {
	m := testManager(t, "cli")
	calls := 0
	addTestPlatform(t, m, "cli", func(_ context.Context, n events.Notification) (events.NotificationReceipt, error) {
		calls++
		if n.Text != "explicit" {
			t.Error("default background notice escaped", n)
		}
		return events.NotificationReceipt{}, nil
	})
	ctx := contextinfo.WithBackgroundKind(t.Context(), contextinfo.BackgroundKindCron)
	ctx = contextinfo.WithConversation(ctx, contextinfo.Conversation{Source: contextinfo.Source{Platform: "cli", ScopeID: "old-origin"}})
	if _, err := events.NotificationRequested.Emit(ctx, events.NewNotification(ctx, slog.LevelInfo, "silent")); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("background notification sent")
	}
	if _, err := events.NotificationRequested.Emit(ctx, events.Notification{Text: "explicit", Target: events.NotificationTarget{Platform: "cli", ScopeID: "target"}}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("explicit background notification suppressed")
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
	sessions := newSessionSupervisor(t, store)
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
