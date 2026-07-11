package control

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"elbot/internal/hook"
	hookruntime "elbot/internal/hook/runtime"
)

// Loader builds a complete candidate hook set and returns the stateful runtime
// configuration discovered alongside it.
type Loader func(hook.Registrar) (hook.ReloadReport, []hookruntime.Config, error)

type Runtime interface {
	Apply([]hookruntime.Config) error
	ValidatePlugin(hookruntime.Config) error
	ReplacePlugin(hookruntime.Config) error
	List() []hookruntime.Info
	Start(string) error
	Stop(context.Context, string) error
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
	if s == nil || s.manager == nil {
		return nil
	}
	return s.manager.List()
}

func (s *Service) HookReload() (hook.ReloadReport, error) {
	if s == nil {
		return hook.ReloadReport{}, fmt.Errorf("hook reloader is not configured")
	}
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	return s.hookReload()
}

func (s *Service) hookReload() (hook.ReloadReport, error) {
	if s == nil || s.manager == nil || s.loader == nil {
		return hook.ReloadReport{}, fmt.Errorf("hook reloader is not configured")
	}
	candidate := hook.NewManager()
	report, configs, err := s.loader(candidate)
	if err != nil {
		return report, err
	}
	if len(configs) > 0 && s.runtime == nil {
		return report, fmt.Errorf("stateful hook runtime is not configured")
	}
	if s.runtime != nil {
		if err := s.runtime.Apply(configs); err != nil {
			return report, err
		}
	}
	s.manager.Replace(candidate)
	return report, nil
}

// PreparePluginReload validates a complete candidate snapshot immediately and
// returns a commit that replaces only the requesting persistent plugin.
func (s *Service) PreparePluginReload(id string) (func() error, error) {
	if s == nil || s.manager == nil || s.runtime == nil || s.loader == nil {
		return nil, fmt.Errorf("hook plugin reloader is not configured")
	}
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
	return func() error {
		s.reloadMu.Lock()
		defer s.reloadMu.Unlock()
		if err := s.runtime.ReplacePlugin(*config); err != nil {
			return err
		}
		s.manager.ReplacePlugin(id, candidate)
		return nil
	}, nil
}

func (s *Service) StatefulHooks() []hookruntime.Info {
	if s == nil || s.runtime == nil {
		return nil
	}
	return s.runtime.List()
}

func (s *Service) StartStatefulHook(id string) error {
	if s == nil || s.runtime == nil {
		return fmt.Errorf("stateful hook runtime is not configured")
	}
	return s.runtime.Start(id)
}

func (s *Service) StopStatefulHook(ctx context.Context, id string) error {
	if s == nil || s.runtime == nil {
		return fmt.Errorf("stateful hook runtime is not configured")
	}
	return s.runtime.Stop(ctx, id)
}

func (s *Service) RestartStatefulHook(ctx context.Context, id string) error {
	if s == nil || s.runtime == nil {
		return fmt.Errorf("stateful hook runtime is not configured")
	}
	return s.runtime.Restart(ctx, id)
}
