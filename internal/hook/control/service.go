// 协调 Hook 查询、重载、启动、停止、重启和候选配置提交。

package control

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"elbot/internal/events"
	"elbot/internal/hook"
	hookruntime "elbot/internal/hook/runtime"
)

// Loader builds a complete candidate hook set and returns the stateful runtime
// configuration discovered alongside it.
type Loader func(hook.Registrar) (hook.ReloadReport, []hookruntime.Config, error)

type Runtime interface {
	Apply([]hookruntime.Config, func(string)) error
	ValidatePlugin(hookruntime.Config) error
	ReplacePlugin(string, *hookruntime.Config, func()) (events.LogResult, error)
	List() []hookruntime.Info
	Start(string) error
	Stop(context.Context, string) (bool, error)
	Restart(context.Context, string) error
}

type Service struct {
	manager  *hook.DefaultManager
	runtime  Runtime
	loader   Loader
	reloadMu sync.Mutex
}

func New(manager *hook.DefaultManager, runtime Runtime, loader Loader) *Service {
	return &Service{manager: manager, runtime: runtime, loader: loader}
}

func (s *Service) HookList() []hook.Info {
	return s.manager.List()
}

func (s *Service) HookReload() (hook.ReloadReport, error) {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	return s.hookReload()
}

func (s *Service) hookReload() (hook.ReloadReport, error) {
	candidate := hook.NewManager()
	report, configs, err := s.loader(candidate)
	if err != nil {
		return report, err
	}
	runtimeIDs := map[string]bool{}
	for _, info := range s.runtime.List() {
		runtimeIDs[info.ID] = true
	}
	for _, config := range configs {
		runtimeIDs[config.ID] = true
	}
	ids := map[string]bool{}
	for _, info := range s.manager.List() {
		ids[info.PluginID] = true
	}
	for _, info := range candidate.List() {
		ids[info.PluginID] = true
	}
	for id := range ids {
		if !runtimeIDs[id] {
			s.manager.ReplacePlugin(id, candidate)
		}
	}
	err = s.runtime.Apply(configs, func(id string) { s.manager.ReplacePlugin(id, candidate) })
	return report, err
}

// PreparePluginReload validates a complete candidate snapshot immediately and
// returns a commit that replaces only the requesting persistent plugin.
// The commit keeps its source log outcome separate from the business error.
func (s *Service) PreparePluginReload(id string) (func() (events.LogResult, error), error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("hook plugin id is required")
	}
	s.reloadMu.Lock()
	candidate := hook.NewManager()
	report, configs, err := s.loader(candidate)
	s.reloadMu.Unlock()
	if err != nil {
		return nil, err
	}
	var config *hookruntime.Config
	for i := range configs {
		if configs[i].ID == id {
			value := configs[i]
			config = &value
			break
		}
	}
	if config == nil {
		detail := strings.TrimSpace(strings.Join(report.Notices, "\n"))
		if detail != "" {
			return nil, fmt.Errorf("stateful hook %q is missing from reloaded config: %s", id, detail)
		}
		return nil, fmt.Errorf("stateful hook %q is missing from reloaded config", id)
	}
	if err := s.runtime.ValidatePlugin(*config); err != nil {
		return nil, err
	}
	return func() (events.LogResult, error) {
		s.reloadMu.Lock()
		defer s.reloadMu.Unlock()
		if err := s.runtime.ValidatePlugin(*config); err != nil {
			return events.LogResult{ResultStatus: events.ResultFailed, Content: err.Error()}, err
		}
		return s.runtime.ReplacePlugin(id, config, func() { s.manager.ReplacePlugin(id, candidate) })
	}, nil
}

func (s *Service) StatefulHooks() []hookruntime.Info {
	return s.runtime.List()
}

func (s *Service) StartStatefulHook(id string) error {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	return s.runtime.Start(id)
}

func (s *Service) StopStatefulHook(ctx context.Context, id string) error {
	_, err := s.StopHook(ctx, id)
	return err
}

func (s *Service) StopHook(ctx context.Context, id string) (bool, error) {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	matched := s.manager.Matches(id)
	stopped := s.manager.Stop(id)
	runtimeStopped, err := s.runtime.Stop(ctx, id)
	if err != nil && (runtimeStopped || !matched) {
		return matched || runtimeStopped || stopped > 0, err
	}
	matched = matched || runtimeStopped
	return matched || stopped > 0, nil
}

func (s *Service) RestartStatefulHook(ctx context.Context, id string) error {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	return s.runtime.Restart(ctx, id)
}
