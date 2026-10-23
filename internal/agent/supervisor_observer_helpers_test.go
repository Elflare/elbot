package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	agentevents "elbot/internal/agent/events"
	"elbot/internal/command"
	"elbot/internal/completion"
	"elbot/internal/delivery"
	"elbot/internal/delivery/dispatch"
	"elbot/internal/events"
	"elbot/internal/notification"
	"elbot/internal/platform"
	"elbot/internal/signal"
)

type assemblyPlatform struct {
	mu        sync.Mutex
	outputs   []string
	catalog   []command.Info
	completer *completion.Service
}

func connectTestNotifications(t *testing.T, router *dispatch.Router) *notification.Manager {
	t.Helper()
	m, err := notification.New("cli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	s, err := m.AddPlatform("cli")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Connect(func(ctx context.Context, n events.Notification) (events.NotificationReceipt, error) {
		r, _, err := router.SendTo(ctx, platform.NotificationTarget(n), []delivery.Output{delivery.Text(n.Text)})
		return events.NotificationReceipt{PlatformMessageIDs: r.PlatformMessageIDs}, err
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (*assemblyPlatform) Name() string { return "cli" }

func (*assemblyPlatform) Run(context.Context, platform.PlatformHandler) error { return nil }

func (p *assemblyPlatform) SendChat(_ context.Context, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.outputs = append(p.outputs, delivery.FallbackOutput(outputs).Text)
	return delivery.Receipt{PlatformMessageIDs: []string{fmt.Sprint(len(p.outputs))}}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
}

func (p *assemblyPlatform) SendTo(ctx context.Context, noticeTarget delivery.Target, noticeOutputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	return p.SendChat(ctx, noticeOutputs)
}

func (p *assemblyPlatform) SetCommandCatalog(infos []command.Info) { p.catalog = infos }

func (p *assemblyPlatform) SetCompleter(service *completion.Service) { p.completer = service }

func (p *assemblyPlatform) text() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.outputs, "\n")
}

func observerSignals() agentevents.Signals {
	return agentevents.Signals{
		ModelCallCompleted: signal.New[agentevents.ModelCallCompletedEvent, signal.Empty]("model"),
		StatusChanged:      signal.New[agentevents.StatusChangedEvent, signal.Empty]("status"),
		VisionFallbackUsed: signal.New[agentevents.VisionFallbackUsedEvent, signal.Empty]("vision"),
		HookFailed:         signal.New[agentevents.HookFailedEvent, signal.Empty]("hook"),
	}
}
