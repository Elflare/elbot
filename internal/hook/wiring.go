// 拆分配置、注入外部能力并装配 Hook 内部组件。

package hook

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"elbot/internal/config"
	"elbot/internal/events"
	"elbot/internal/hook/control"
	"elbot/internal/hook/pipeline"
	"elbot/internal/hook/rules"
	hookruntime "elbot/internal/hook/runtime"
	"elbot/internal/hook/share"
	"elbot/internal/processenv"
)

func NewSupervisor(ctx context.Context, cfg *config.Config, deps SupervisorDeps) (*Supervisor, error) {
	if deps.Control == nil {
		return nil, fmt.Errorf("hook: Control is required")
	}
	configDir := config.PluginConfigDir(cfg.ConfigPath)
	runtime, err := hookruntime.NewManager(hookruntime.Options{
		Media:     deps.Media,
		Tools:     deps.Tools,
		Send:      deps.Platform.SendTo,
		Log:       events.AsyncLog,
		Notify:    events.Notify,
		SharedDir: filepath.Join(configDir, "_shared"),
	})
	if err != nil {
		return nil, err
	}
	manager := pipeline.NewManager(events.AsyncLog, runtime.Route)
	opts := rules.Options{
		BuildMessageSegments: runtime.BuildMessageSegments,
		ConfigDir:            configDir,
		Tools:                deps.Tools,
		Send:                 deps.Platform.SendTo,
		CallPlatformAPI:      deps.Platform.CallPlatformAPI,
		Runtime:              runtime,
		ProcessEnv:           processenv.New(os.Environ()),
		Log:                  events.AsyncLog,
		Notify:               events.Notify,
	}
	loadHooks := func() (share.ReloadReport, control.ReloadCandidate, error) {
		candidate := pipeline.NewManager(events.AsyncLog, nil)
		var notices []string
		loadOpts := opts
		loadOpts.Notices = &notices
		module, err := rules.NewModule(loadOpts)
		report := share.ReloadReport{Notices: notices}
		if err != nil {
			return report, control.ReloadCandidate{}, err
		}
		module.RegisterHooks(candidate)
		return report, control.ReloadCandidate{
			Infos: candidate.List(), Runtimes: module.Runtimes,
			Commit: func(pluginID string) { manager.ReplacePlugin(pluginID, candidate) },
		}, nil
	}
	service := control.New(manager, runtime, loadHooks)
	runtime.SetPluginReloadPreparer(service.PreparePluginReload)
	// App cancels its producers before Hook.Close. Their final callbacks must
	// retain access to Hook until that explicit shutdown boundary.
	callCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	m := &Supervisor{
		manager: manager, runtime: runtime, control: service,
		send:   deps.Platform.SendTo,
		parent: ctx, ctx: callCtx, cancel: cancel, done: make(chan struct{}),
	}
	if err := deps.Control.ConnectHooks(management{supervisor: m}); err != nil {
		return m, err
	}
	return m, nil
}
