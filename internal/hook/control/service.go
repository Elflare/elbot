package control

import (
	"context"
	"fmt"

	"elbot/internal/hook"
	hookruntime "elbot/internal/hook/runtime"
)

// Loader builds a complete candidate hook set and returns the stateful runtime
// configuration discovered alongside it.
type Loader func(hook.Registrar) (hook.ReloadReport, []hookruntime.Config, error)

type Runtime interface {
	Apply([]hookruntime.Config) error
	List() []hookruntime.Info
	Start(string) error
	Stop(context.Context, string) error
	Restart(context.Context, string) error
}

type Service struct {
	manager *hook.DefaultManager
	runtime Runtime
	loader  Loader
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
