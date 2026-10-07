package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"elbot/internal/agent"
	"elbot/internal/command"
	"elbot/internal/completion"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/platform"
	platformbuiltin "elbot/internal/platform/builtin"
)

type defaultPlatformFactory struct{}

func (defaultPlatformFactory) Build(req PlatformRequest) (PlatformComponents, error) {
	foundation := req.Foundation
	bundle, err := platformbuiltin.New(platformbuiltin.Options{Mode: platformMode(req.Mode)}, foundation.Config, foundation.Store, foundation.ChatHistory)
	if err != nil {
		return PlatformComponents{}, err
	}
	req.Profiler.Mark("platform init")
	return PlatformComponents{Primary: bundle.Primary, Runtimes: bundle.Runtimes}, nil
}

type defaultPlatformExecutor struct{}

func (defaultPlatformExecutor) Run(ctx context.Context, req PlatformRunRequest) error {
	return runPlatforms(ctx, req.Handler, req.Runtimes, req.AfterStart, req.Stop)
}

type platformRuntime = platform.Runtime

type platformLifecycle interface {
	StopAppOnExit() bool
}

type platformHookAgent interface {
	NotifyPlatformConnected(ctx context.Context, platformName string)
}

type completionPlatform interface {
	SetCompleter(*completion.Service)
}

type commandCatalogPlatform interface {
	SetCommandCatalog([]command.Info)
}

func registerCompletionPlatforms(agt *agent.Agent, adapters []platformRuntime) {
	if agt == nil {
		return
	}
	for _, adapter := range adapters {
		if adapter == nil {
			continue
		}
		if completer, ok := adapter.(completionPlatform); ok {
			completer.SetCompleter(agt.CompletionService())
		}
	}
}

func registerCommandCatalogs(router *command.Router, adapters []platformRuntime) {
	if router == nil {
		return
	}
	commands := router.Commands()
	for _, adapter := range adapters {
		if adapter == nil {
			continue
		}
		if catalog, ok := adapter.(commandCatalogPlatform); ok {
			catalog.SetCommandCatalog(commands)
		}
	}
}

func platformStopsAppOnExit(adapter platformRuntime) bool {
	lifecycle, ok := adapter.(platformLifecycle)
	return ok && lifecycle.StopAppOnExit()
}

func runPlatforms(ctx context.Context, handler platform.PlatformHandler, adapters []platformRuntime, afterStart func(context.Context), stop context.CancelFunc) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	for _, adapter := range adapters {
		adapter := adapter
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := adapter.Run(runCtx, handler); err != nil && !errors.Is(err, context.Canceled) {
				_ = globalevents.EmitLog(runCtx, globalevents.LogRecord{
					Category: globalevents.LogRuntime,
					Level:    slog.LevelWarn,
					Name:     "platform_stopped_with_error",
					Module:   "app",
					Summary:  "platform stopped with error",
					Fields:   []slog.Attr{slog.Any("platform", adapter.Name()), slog.Any("error", err.Error())},
				})
			}
			if platformStopsAppOnExit(adapter) {
				cancel()
				if stop != nil {
					stop()
				}
			}
		}()
	}
	if afterStart != nil {
		afterStart(runCtx)
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		cancel()
		<-done
		return ctx.Err()
	case <-done:
		return nil
	}
}

type elnisRuntimeAdapter struct {
	runtime interface {
		Run(context.Context) error
	}
}

func (a elnisRuntimeAdapter) Name() string { return "elnis" }

func (a elnisRuntimeAdapter) Run(ctx context.Context, _ platform.PlatformHandler) error {
	return a.runtime.Run(ctx)
}

func (a elnisRuntimeAdapter) SendChat(context.Context, []delivery.Output) (delivery.Receipt, error) {
	return delivery.Receipt{}, fmt.Errorf("elnis cannot send chat output")
}

func (a elnisRuntimeAdapter) SendNotice(context.Context, delivery.Notice) (delivery.Receipt, error) {
	return delivery.Receipt{}, fmt.Errorf("elnis cannot send notice output")
}
