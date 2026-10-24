package hook

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/signal"
)

type wiringTestPlatform struct{}

func (wiringTestPlatform) SendTo(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	return delivery.Receipt{}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
}

func (wiringTestPlatform) CallPlatformAPI(context.Context, string, string, map[string]any) (json.RawMessage, error) {
	return nil, nil
}

func TestSupervisorLoadsRulesAtStartAndKeepsConfigFailureNonFatal(t *testing.T) {
	cfg := &config.Config{ConfigPath: filepath.Join(t.TempDir(), "config.toml")}
	dir := config.PluginConfigDir(cfg.ConfigPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hooks.toml"), []byte("[[rules]]\nname = 'bad'\non = 'agent.out.prepared'\nalways = true\naction = 'append'\nfield = 'message.text'\ntext = '!'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	notices := make(chan string, 2)
	connection, err := events.NotificationRequested.Connect(func(_ context.Context, notice events.Notification) (events.NotificationReceipt, error) {
		notices <- notice.Text
		return events.NotificationReceipt{}, nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(context.Background())
	m, err := NewSupervisor(t.Context(), cfg, SupervisorDeps{Platform: wiringTestPlatform{}})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(context.Background())
	select {
	case text := <-notices:
		t.Fatalf("constructor loaded rules: %s", text)
	default:
	}
	if err := m.ConnectAgent(&bridgeTestAgent{}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := m.Start(); err != nil {
			t.Fatalf("configuration failure became fatal: %v", err)
		}
	}
	select {
	case text := <-notices:
		if !strings.Contains(text, "配置错误") {
			t.Fatal(text)
		}
	case <-time.After(time.Second):
		t.Fatal("missing configuration notice")
	}
	if infos := m.HookList(); len(infos) != 0 {
		t.Fatalf("invalid rules registered: %+v", infos)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); !errors.Is(err, context.Canceled) {
		t.Fatalf("closed supervisor restarted: %v", err)
	}
}
