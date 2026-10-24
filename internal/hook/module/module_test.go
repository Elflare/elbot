// 待删除：验证旧 Hook 模块构造和生命周期，相关覆盖将迁入根包测试。

package module

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"elbot/internal/events"
	"elbot/internal/hook"
	hookcontrol "elbot/internal/hook/control"
	hookruntime "elbot/internal/hook/runtime"
	"elbot/internal/signal"
	"elbot/internal/tool"
)

func TestInitialReloadWaitsForConnectionsAndBeginClosePreservesCore(t *testing.T) {
	m, err := NewModule(t.Context(), ModuleDeps{ConfigPath: filepath.Join(t.TempDir(), "config.toml"), Registry: tool.NewRegistry()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	defer m.Close(ctx)
	connected, loads := false, 0
	m.control = hookcontrol.New(m.manager, m.runtime, func(hook.Registrar) (hook.ReloadReport, []hookruntime.Config, error) {
		loads++
		if !connected {
			t.Error("reload before cross-line connections")
		}
		return hook.ReloadReport{}, nil, nil
	})
	m.SetWakeup(func(context.Context, hook.Event) bool { return true })
	m.SetObserver(func(ctx context.Context, _ hook.Event, _ hook.ObserverInfo) (context.Context, func()) {
		return ctx, func() {}
	})
	connected = true
	for range 2 {
		if err := m.Start(); err != nil {
			t.Fatal(err)
		}
	}
	if loads != 1 {
		t.Fatal("initial reload repeated", loads)
	}
	m.BeginClose()
	select {
	case <-m.Done():
		t.Fatal("Hook core closed before Dialogue final callbacks")
	default:
	}
	if err := m.Router().Apply(nil, func(string) {}); err != nil {
		t.Fatal("core unavailable before ordered Close", err)
	}
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); !errors.Is(err, context.Canceled) {
		t.Fatal("closed module restarted", err)
	}
}
func TestInitialReloadKeepsWarningAndContinuePolicy(t *testing.T) {
	got := make(chan string, 2)
	c, err := events.NotificationRequested.Connect(func(_ context.Context, n events.Notification) (events.NotificationReceipt, error) {
		got <- n.Text
		return events.NotificationReceipt{}, nil
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	m, err := NewModule(t.Context(), ModuleDeps{ConfigPath: filepath.Join(t.TempDir(), "config.toml"), Registry: tool.NewRegistry()})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(context.Background())
	m.control = hookcontrol.New(m.manager, m.runtime, func(hook.Registrar) (hook.ReloadReport, []hookruntime.Config, error) {
		_, _ = events.NotificationRequested.EmitAsync(context.Background(), events.Notification{Text: "candidate notice"})
		return hook.ReloadReport{Notices: []string{"candidate notice"}}, nil, errors.New("plugin rejected")
	})
	if err := m.Start(); err != nil {
		t.Fatal("plugin failure became fatal", err)
	}
	var notices []string
	for range 1 {
		select {
		case text := <-got:
			notices = append(notices, text)
		case <-time.After(time.Second):
			t.Fatal("missing notification")
		}
	}
	if notices[0] != "candidate notice" {
		t.Fatal(notices)
	}
}
