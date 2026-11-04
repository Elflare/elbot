package agent

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/platform/dispatch"
	"elbot/internal/platform/notification"
	"elbot/internal/signal"
	"fmt"
	"strings"
	"sync"
	"testing"
)

type assemblyPlatform struct {
	mu      sync.Mutex
	outputs []string
}

func connectTestNotifications(t *testing.T, router *dispatch.Router) *notification.Manager {
	t.Helper()
	m, err := notification.New("cli", func(context.Context, events.LogRecord) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	s, err := m.AddPlatform("cli", func(events.NotificationTarget) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Observe(func(ctx context.Context, n events.Notification) error {
		_, err := router.SendTo(ctx, delivery.Target{Platform: n.Target.Platform, ChatID: n.Target.ID, ChatType: n.Target.ChatType}, []delivery.Output{delivery.Text(n.Text)})
		return err
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (*assemblyPlatform) Name() string { return "cli" }

func (p *assemblyPlatform) SendTo(_ context.Context, _ delivery.Target, outputs []delivery.Output) (delivery.Receipt, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.outputs = append(p.outputs, delivery.FallbackOutput(outputs).Text)
	return delivery.Receipt{PlatformMessageIDs: []string{fmt.Sprint(len(p.outputs))}}, nil
}

func (p *assemblyPlatform) text() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.outputs, "\n")
}
