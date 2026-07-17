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
	elcron "elbot/internal/cron"
	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/platform"
	platformbuiltin "elbot/internal/platform/builtin"
)

type defaultPlatformFactory struct{}

func (defaultPlatformFactory) Build(req PlatformRequest) (PlatformComponents, error) {
	foundation := req.Foundation
	bundle, err := platformbuiltin.New(
		platformbuiltin.Options{Mode: platformMode(req.Mode)},
		foundation.Config,
		foundation.Store,
		foundation.ChatHistory,
		foundation.Logger,
	)
	if err != nil {
		return PlatformComponents{}, err
	}
	req.Profiler.Mark("platform init")
	return PlatformComponents{Primary: bundle.Primary, Runtimes: bundle.Runtimes}, nil
}

type defaultPlatformExecutor struct{}

func (defaultPlatformExecutor) Run(ctx context.Context, req PlatformRunRequest) error {
	return runPlatforms(ctx, req.Handler, req.Logger, req.Runtimes, req.AfterStart)
}

type platformRuntime = platform.Runtime

type platformLifecycle interface {
	StopAppOnExit() bool
}

type platformHookAgent interface {
	RegisterPlatformSender(name string, sender delivery.MessageSender)
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

func registerCronPlatformHook(hooks hook.Registrar, service *elcron.Service) error {
	if hooks == nil || service == nil {
		return nil
	}
	return hooks.Register(hook.Registration{
		Point:       hook.PointPlatformConnected,
		Name:        "builtin.cron.missed_once",
		Description: "平台连接时补投递 missed once cron",
		Match:       hook.Always(),
		Handler: hook.HandlerFunc(func(ctx context.Context, event hook.Event) (hook.Event, error) {
			service.NotifyPlatformConnected(ctx, event.Platform.Name)
			return event, nil
		}),
	})
}

func registerCommandCatalogs(agt *agent.Agent, adapters []platformRuntime) {
	if agt == nil {
		return
	}
	commands := agt.CommandInfos()
	for _, adapter := range adapters {
		if adapter == nil {
			continue
		}
		if catalog, ok := adapter.(commandCatalogPlatform); ok {
			catalog.SetCommandCatalog(commands)
		}
	}
}

func registerPlatformHooks(agt platformHookAgent, adapters []platformRuntime) {
	if agt == nil {
		return
	}
	for _, adapter := range adapters {
		if adapter == nil {
			continue
		}
		agt.RegisterPlatformSender(adapter.Name(), adapter)
		if notifier, ok := adapter.(platform.ConnectNotifier); ok {
			name := adapter.Name()
			notifier.SetConnectNotifier(func(ctx context.Context, platformName string) {
				if platformName == "" {
					platformName = name
				}
				agt.NotifyPlatformConnected(ctx, platformName)
			})
		}
	}
}

func platformStopsAppOnExit(adapter platformRuntime) bool {
	lifecycle, ok := adapter.(platformLifecycle)
	return ok && lifecycle.StopAppOnExit()
}

func runPlatforms(ctx context.Context, handler platform.PlatformHandler, logger *slog.Logger, adapters []platformRuntime, afterStart func(context.Context)) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	for _, adapter := range adapters {
		adapter := adapter
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := adapter.Run(runCtx, handler); err != nil && !errors.Is(err, context.Canceled) && logger != nil {
				logger.WarnContext(runCtx, "platform stopped with error", "platform", adapter.Name(), "error", err.Error())
			}
			if platformStopsAppOnExit(adapter) {
				cancel()
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

func (a elnisRuntimeAdapter) SendNotice(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, error) {
	return delivery.Receipt{}, fmt.Errorf("elnis cannot send notice output")
}
