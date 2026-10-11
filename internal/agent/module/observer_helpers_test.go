package module

import (
	"context"
	"fmt"
	"strings"
	"sync"

	agentevents "elbot/internal/agent/events"
	"elbot/internal/command"
	"elbot/internal/completion"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/platform"
	"elbot/internal/signal"
)

type assemblyPlatform struct {
	mu        sync.Mutex
	outputs   []string
	catalog   []command.Info
	completer *completion.Service
}

func (*assemblyPlatform) Name() string                                        { return "cli" }
func (*assemblyPlatform) Run(context.Context, platform.PlatformHandler) error { return nil }
func (p *assemblyPlatform) SendChat(_ context.Context, outputs []delivery.Output) (delivery.Receipt, events.LogResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.outputs = append(p.outputs, delivery.FallbackOutput(outputs).Text)
	return delivery.Receipt{PlatformMessageIDs: []string{fmt.Sprint(len(p.outputs))}}, events.LogResult{ResultStatus: events.ResultSucceeded}, nil
}
func (p *assemblyPlatform) SendNotice(ctx context.Context, notice delivery.Notice) (delivery.Receipt, events.LogResult, error) {
	return p.SendChat(ctx, notice.Outputs)
}
func (p *assemblyPlatform) SetCommandCatalog(infos []command.Info)   { p.catalog = infos }
func (p *assemblyPlatform) SetCompleter(service *completion.Service) { p.completer = service }
func (p *assemblyPlatform) text() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.outputs, "\n")
}

func observerSignals() agentevents.Signals {
	return agentevents.Signals{
		ModelCallCompleted: signal.New[agentevents.ModelCallCompletedEvent]("model"),
		StatusChanged:      signal.New[agentevents.StatusChangedEvent]("status"),
		VisionFallbackUsed: signal.New[agentevents.VisionFallbackUsedEvent]("vision"),
		HookFailed:         signal.New[agentevents.HookFailedEvent]("hook"),
	}
}
