package platform

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"slices"
	"sync"
	"testing"
	"testing/synctest"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/signal"
	"elbot/internal/testutil"
)

func TestFailureNotificationWiringSurvivesIngressStop(t *testing.T) {
	store := testutil.NewSQLiteStore(t)
	synctest.Test(t, func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Security.Superadmins = map[string][]string{
			"healthy": {" one ", "one", "", "two"},
			"failed":  {"failed-admin"},
			"stopped": {"stopped-admin"},
		}
		m, err := NewSupervisor(context.Background(), cfg, SupervisorDeps{Mode: "service", Store: store})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := m.Close(context.Background()); err != nil {
				t.Error(err)
			}
		})
		var mu sync.Mutex
		var destinations []string
		var failures []events.LogRecord
		logs, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
			if record.EventName == "platform_failed" {
				mu.Lock()
				failures = append(failures, record)
				mu.Unlock()
			}
			return nil
		}, signal.ConnectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer logs.Close(context.Background())
		for _, name := range []string{"healthy", "failed", "stopped"} {
			runtime, _ := testRuntime(name, false, nil)
			runtime.running = name != "stopped"
			m.runtimes = append(m.runtimes, runtime)
			queue, err := m.notifications.AddPlatform(name)
			if err != nil {
				t.Fatal(err)
			}
			connection, err := queue.Connect(func(ctx context.Context, n events.Notification) (events.NotificationReceipt, error) {
				if ctx.Err() != nil || n.Target.Kind != contextinfo.ConversationPrivate || n.Level != slog.LevelError || n.Text != "平台 failed 异常退出：offline" {
					t.Errorf("unexpected notification: %+v, context error: %v", n, ctx.Err())
				}
				mu.Lock()
				destinations = append(destinations, n.Target.Platform+":"+n.Target.ID)
				mu.Unlock()
				return events.NotificationReceipt{PlatformMessageIDs: []string{"sent"}}, nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			m.connections = append(m.connections, connection)
		}
		m.configureCloseSteps()
		// 根部必须使用构造时的配置快照，并保留停止接入后的通知能力。
		cfg.Security.Superadmins["healthy"][0] = "changed"
		m.BeginClose()
		if !errors.Is(m.ctx.Err(), context.Canceled) {
			t.Fatal("ingress context was not canceled")
		}
		m.onFailure("failed", errors.New("offline"))
		synctest.Wait()
		mu.Lock()
		slices.Sort(destinations)
		if !reflect.DeepEqual(destinations, []string{"healthy:one", "healthy:two"}) {
			t.Errorf("failure destinations: %v", destinations)
		}
		if len(failures) != 1 || failures[0].Level != slog.LevelError || failures[0].Content != "failed: offline" {
			t.Errorf("failure should be logged once by root: %+v", failures)
		}
		mu.Unlock()
		if err := m.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		select {
		case <-m.Done():
		default:
			t.Fatal("root did not complete final cleanup")
		}
		for _, connection := range m.connections {
			select {
			case <-connection.Done():
			default:
				t.Fatal("root retained a notification receiver connection")
			}
		}
	})
}
