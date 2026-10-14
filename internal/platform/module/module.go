package module

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"elbot/internal/command"
	"elbot/internal/completion"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery/dispatch"
	globalevents "elbot/internal/events"
	"elbot/internal/notification"
	"elbot/internal/platform"
	platformbuiltin "elbot/internal/platform/builtin"
	"elbot/internal/platform/cli"
)

type Module struct {
	ui              *cli.UI
	primary         platform.PlatformAdapter
	runtimes        []platform.Runtime
	dispatcher      *dispatch.Router
	notifications   *notification.Manager
	ctx             context.Context
	cancel          context.CancelFunc
	mu              sync.Mutex
	started, closed bool
	done            chan struct{}
	result          error
	mode            platformbuiltin.Mode // Service mode outlives all message platform loops.
	superadmins     map[string][]string  // Configured notice recipients, keyed by runtime platform name.
	running         map[string]bool      // Loops not yet exited; protected by mu.
}

func NewModule(ctx context.Context, deps ModuleDeps) (*Module, error) {
	ctx, cancel := context.WithCancel(ctx)
	m := &Module{ctx: ctx, cancel: cancel, done: make(chan struct{}), mode: deps.Mode, superadmins: deps.Superadmins}
	cfg := &config.Config{ConfigPath: deps.ConfigPath, Platform: deps.Platform, Security: config.SecurityConfig{Superadmins: deps.Superadmins}, Commands: config.CommandsConfig{Prefixes: deps.Prefixes}, Sandbox: config.SandboxConfig{Root: deps.SandboxRoot}, PlatformFiles: deps.Files}
	bundle, err := platformbuiltin.New(platformbuiltin.Options{Mode: deps.Mode}, cfg, deps.Store, deps.ChatHistory)
	if err != nil {
		return m, err
	}
	m.primary, m.runtimes = bundle.Primary, bundle.Runtimes
	m.dispatcher = dispatch.New(dispatch.Options{Primary: m.primary, Store: deps.Store, Media: deps.Media, MediaRetentionDays: deps.MediaRetentionDays})
	for _, adapter := range m.runtimes {
		m.dispatcher.RegisterPlatformSender(adapter.Name(), adapter)
	}
	return m, m.connectNotifications()
}
func (m *Module) Start(handler platform.PlatformHandler, stop context.CancelFunc) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return context.Canceled
	}
	if m.started {
		return nil
	}
	m.started = true
	m.running = make(map[string]bool, len(m.runtimes))
	for _, adapter := range m.runtimes {
		m.running[adapter.Name()] = true
	}
	launched := make(chan struct{})
	go func() {
		err := m.runPlatforms(m.ctx, handler, func(context.Context) { close(launched) }, stop)
		if m.ui != nil {
			err = errors.Join(err, m.ui.Close(context.Background()))
		}
		err = errors.Join(err, m.notifications.Close(context.Background()))
		m.mu.Lock()
		m.result = err
		close(m.done)
		m.mu.Unlock()
	}()
	<-launched
	return nil
}

// Wait includes service lifetime even when no message platform remains running.
func (m *Module) Wait(ctx context.Context) error {
	select {
	case <-m.done:
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.result
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m *Module) PrimaryName() string          { return m.primary.Name() }
func (m *Module) Dispatcher() *dispatch.Router { return m.dispatcher }
func (m *Module) Names() []string {
	out := make([]string, 0, len(m.runtimes))
	for _, r := range m.runtimes {
		out = append(out, r.Name())
	}
	return out
}
func (m *Module) SetCompleter(service *completion.Service) {
	registerCompletionPlatforms(service, m.runtimes)
}
func (m *Module) SetCommandCatalog(router *command.Router) {
	registerCommandCatalogs(router, m.runtimes)
}

type platformLifecycle interface {
	StopAppOnExit() bool
}

type completionPlatform interface {
	SetCompleter(*completion.Service)
}

type commandCatalogPlatform interface {
	SetCommandCatalog([]command.Info)
}

func registerCompletionPlatforms(service *completion.Service, adapters []platform.Runtime) {
	for _, adapter := range adapters {
		if completer, ok := adapter.(completionPlatform); ok {
			completer.SetCompleter(service)
		}
	}
}

func registerCommandCatalogs(router *command.Router, adapters []platform.Runtime) {
	commands := router.Commands()
	for _, adapter := range adapters {
		if catalog, ok := adapter.(commandCatalogPlatform); ok {
			catalog.SetCommandCatalog(commands)
		}
	}
}

func platformStopsAppOnExit(adapter platform.Runtime) bool {
	lifecycle, ok := adapter.(platformLifecycle)
	return ok && lifecycle.StopAppOnExit()
}

func (m *Module) runPlatforms(ctx context.Context, handler platform.PlatformHandler, afterStart func(context.Context), stop context.CancelFunc) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	for _, adapter := range m.runtimes {
		adapter := adapter
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := adapter.Run(runCtx, handler)
			m.mu.Lock()
			delete(m.running, adapter.Name())
			m.mu.Unlock()
			if err != nil && !(runCtx.Err() != nil && errors.Is(err, runCtx.Err())) {
				{
					record := globalevents.LogRecord{
						Category:     globalevents.LogRuntime,
						Level:        slog.LevelError,
						EventName:    "platform_stopped_with_error",
						Module:       "app",
						Type:         globalevents.ContentText,
						Content:      fmt.Sprintf("platform stopped with error\nplatform=%v\nerror=%v", adapter.Name(), err.Error()),
						ContentLimit: 4096,
						ResultStatus: globalevents.ResultFailed,
					}
					facts, _ := contextinfo.ExecutionFromContext(runCtx)
					if facts.SessionID != "" {
						record.Identity.SessionID = &facts.SessionID
					}
					if facts.RunID != "" {
						record.Identity.RunID = &facts.RunID
					}
					if facts.Attempt != "" {
						record.Identity.Attempt = &facts.Attempt
					}
					if facts.RequestID != "" {
						record.Identity.RequestID = &facts.RequestID
					}
					if facts.RootRequestID != "" {
						record.Identity.RootRequestID = &facts.RootRequestID
					}
					_ = globalevents.EmitLog(context.WithoutCancel(runCtx), record)
				}
				if runCtx.Err() == nil {
					m.notifyPlatformFailure(runCtx, adapter.Name(), err)
				}
			}
			if m.mode != platformbuiltin.ModeService && platformStopsAppOnExit(adapter) {
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
		if m.mode == platformbuiltin.ModeService {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
}
