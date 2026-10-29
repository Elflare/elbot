package agent

import (
	"context"
	"elbot/internal/agent/dialogue"
	agentexecution "elbot/internal/agent/execution"
	agentshare "elbot/internal/agent/share"
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
		r, _, err := router.SendTo(ctx, delivery.Target{Platform: n.Target.Platform, ScopeID: n.Target.ScopeID}, []delivery.Output{delivery.Text(n.Text)})
		return events.NotificationReceipt{PlatformMessageIDs: r.PlatformMessageIDs}, err
	}, signal.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (*assemblyPlatform) Name() string { return "cli" }

func (p *assemblyPlatform) SendChat(_ context.Context, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.outputs = append(p.outputs, delivery.FallbackOutput(outputs).Text)
	return delivery.Receipt{PlatformMessageIDs: []string{fmt.Sprint(len(p.outputs))}}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
}

func (p *assemblyPlatform) SendTo(ctx context.Context, noticeTarget delivery.Target, noticeOutputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	return p.SendChat(ctx, noticeOutputs)
}

func (p *assemblyPlatform) text() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.outputs, "\n")
}

func observerSignals() signals {
	return signals{
		ModelCallCompleted: signal.New[agentshare.ModelCallCompletedEvent, signal.Empty]("model"),
		StatusChanged:      signal.New[agentexecution.StatusChangedEvent, signal.Empty]("status"),
		VisionFallbackUsed: signal.New[dialogue.VisionFallbackUsedEvent, signal.Empty]("vision"),
	}
}
