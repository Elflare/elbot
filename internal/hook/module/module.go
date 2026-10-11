package module

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"

	"elbot/internal/config"
	globalevents "elbot/internal/events"
	"elbot/internal/hook"
	hookbuiltin "elbot/internal/hook/builtin"
	hookcontrol "elbot/internal/hook/control"
	hookruntime "elbot/internal/hook/runtime"
)

// Module owns Hook core and plugin runtimes; Dialogue supplies its request adapter separately.
type Module struct {
	manager         *hook.DefaultManager
	runtime         *hookruntime.Manager
	control         *hookcontrol.Service
	mu              sync.Mutex
	started, closed bool
}

func NewModule(_ context.Context, deps ModuleDeps) (*Module, error) {
	hooks := hook.NewManager()
	hookRuntime := hookruntime.NewManager(hookruntime.Options{
		Media:      deps.Media,
		Registry:   deps.Registry,
		Send:       deps.Send,
		SharedDir:  filepath.Join(config.PluginConfigDir(deps.ConfigPath), "_shared"),
		ProcessEnv: deps.ProcessEnv,
	})
	sendChat, hookProcessEnv := deps.Send, deps.ProcessEnv
	hookOpts := hookbuiltin.Options{
		ConfigDir:       config.PluginConfigDir(deps.ConfigPath),
		Tools:           deps.Registry,
		Send:            sendChat,
		PlatformCallers: deps.PlatformCallers,
		Runtime:         hookRuntime,
		ProcessEnv:      hookProcessEnv,
	}

	loadHooks := func(registrar hook.Registrar) (hook.ReloadReport, []hookruntime.Config, error) {
		var notices []string
		loadOpts := hookOpts
		loadOpts.Notices = &notices
		configs, err := hookbuiltin.RegisterAll(registrar, loadOpts)
		return hook.ReloadReport{Notices: notices}, configs, err
	}
	hookService := hookcontrol.New(hooks, hookRuntime, loadHooks)
	hookRuntime.SetPluginReloadPreparer(hookService.PreparePluginReload)
	return &Module{manager: hooks, runtime: hookRuntime, control: hookService}, nil
}
func (m *Module) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return context.Canceled
	}
	if m.started {
		return nil
	}
	m.started = true
	report, err := m.control.HookReload()
	for _, notice := range report.Notices {
		_, _ = globalevents.NotificationRequested.EmitAsync(context.Background(), globalevents.Notification{Text: notice, Level: slog.LevelWarn})
	}
	if err != nil {
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelError,
				EventName:    "hook_registration_failed",
				Module:       "app",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("hook registration failed\nerror=%v", err),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultFailed,
			}
			if errors.Is(err, context.Canceled) {
				record.ResultStatus = globalevents.ResultCanceled
				record.Level = slog.LevelInfo
			}
			_ = globalevents.EmitLog(context.Background(), record)
		}
		_, _ = globalevents.NotificationRequested.EmitAsync(context.Background(), globalevents.Notification{Text: fmt.Sprintf("Hook 注册失败：%v", err), Level: slog.LevelWarn})
	}

	return nil
}
func (m *Module) Runner() *hook.DefaultManager  { return m.manager }
func (m *Module) Router() *hookruntime.Manager  { return m.runtime }
func (m *Module) Control() *hookcontrol.Service { return m.control }
func (m *Module) SetWakeup(fn hook.WakeupFunc)  { m.manager.SetWakeupFunc(fn) }
func (m *Module) SetObserver(fn hook.Observer)  { m.manager.SetObserver(fn) }
