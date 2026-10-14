package module

import (
	"context"
	"path/filepath"
	"sync"

	"elbot/internal/config"
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
	hookRuntime, err := hookruntime.NewManager(hookruntime.Options{
		Media:     deps.Media,
		Registry:  deps.Registry,
		Send:      deps.Send,
		SharedDir: filepath.Join(config.PluginConfigDir(deps.ConfigPath), "_shared"),
	})
	if err != nil {
		return nil, err
	}
	hookOpts := hookbuiltin.Options{
		ConfigDir:       config.PluginConfigDir(deps.ConfigPath),
		Tools:           deps.Registry,
		Send:            deps.Send,
		PlatformCallers: deps.PlatformCallers,
		Runtime:         hookRuntime,
		ProcessEnv:      deps.ProcessEnv,
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
	_, _ = m.control.HookReload()

	return nil
}
func (m *Module) Runner() *hook.DefaultManager  { return m.manager }
func (m *Module) Router() *hookruntime.Manager  { return m.runtime }
func (m *Module) Control() *hookcontrol.Service { return m.control }
func (m *Module) SetWakeup(fn hook.WakeupFunc)  { m.manager.SetWakeupFunc(fn) }
func (m *Module) SetObserver(fn hook.Observer)  { m.manager.SetObserver(fn) }
