package platform

import (
	"context"
	agentshare "elbot/internal/agent/share"
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
	"elbot/internal/platform/headless"
	"elbot/internal/platform/share"
	"elbot/internal/signal"
	"elbot/internal/testutil"
)

func TestFailureNotificationWiringSurvivesIngressStop(t *testing.T) {
	store := testutil.NewSQLiteStore(t)
	synctest.Test(t, func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Security.Superadmins = map[string][]string{
			"healthy": {"one", "one", "two"},
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
			queue, err := m.notifications.AddPlatform(name, share.ValidateNotificationTarget)
			if err != nil {
				t.Fatal(err)
			}
			connection, err := queue.Connect(func(ctx context.Context, n events.Notification) (events.NotificationReceipt, error) {
				if ctx.Err() != nil || n.Target.ChatType != contextinfo.ChatPrivate || n.Level != slog.LevelError || n.Text != "平台 failed 异常退出：offline" {
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

func TestServiceNotificationBusinessIgnoresLogFailure(t *testing.T) {
	m, err := NewSupervisor(t.Context(), &config.Config{}, SupervisorDeps{Mode: "service", Store: testutil.NewSQLiteStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if len(m.connections) != 1 {
		t.Fatalf("headless notification connections=%d", len(m.connections))
	}
	var records []events.LogRecord
	fault := errors.New("logger rejected notice")
	c, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
		if record.EventName == "runtime_notification" {
			records = append(records, record)
			return fault
		}
		return nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	results, err := events.NotificationRequested.Emit(t.Context(), events.Notification{Text: "host notice", Level: slog.LevelWarn})
	if err != nil || len(results) != 1 || !results[0].Started {
		t.Fatalf("log completion lost: %v %v", results, err)
	}
	if len(records) != 1 || records[0].Content != "host notice" || records[0].Level != slog.LevelWarn {
		t.Fatalf("records=%+v", records)
	}
	if err := headless.ValidateNotificationTarget(events.NotificationTarget{ID: "chat"}); err == nil {
		t.Fatal("headless accepted a conversation destination")
	}
	results, err = events.NotificationRequested.Emit(t.Context(), events.Notification{Text: "invalid", Target: events.NotificationTarget{Platform: "service", ChatType: contextinfo.ChatPrivate, ID: "chat"}})
	if err == nil || len(results) != 1 || results[0].Started || len(records) != 1 {
		t.Fatalf("invalid host destination: %v %v", results, err)
	}
	agentshare.ReportFailure(t.Context(), "session", errors.New("ordinary model failure"), func(ctx context.Context, level slog.Level, text string) {
		_, _ = events.NotificationRequested.Emit(ctx, events.Notification{Text: text, Level: level})
	})
	if len(records) != 2 || records[1].Level != slog.LevelWarn || records[1].Content != "对话处理失败：ordinary model failure" {
		t.Fatalf("headless reclassified an ordinary failure: %+v", records)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, connection := range m.connections {
		select {
		case <-connection.Done():
		default:
			t.Fatal("headless receiver not closed")
		}
	}
}
