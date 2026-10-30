// 协调 Hook 查询、重载、启动、停止、重启和候选配置提交。

package control

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"elbot/internal/events"
	"elbot/internal/hook/share"
)

// ReloadCandidate 是本次加载的候选信息；Commit 持有候选直到提交能力被释放。
type ReloadCandidate struct {
	Infos    []share.Info          // 候选规则的查询快照，用于确定需要替换的插件。
	Runtimes []share.RuntimeConfig // 本次加载的插件运行配置。
	Commit   func(pluginID string) // 仅替换指定插件的规则；候选执行器由根部闭包持有。
}

// Loader 由根部装配，创建完整候选；加载失败时不得提交候选。
type Loader func() (share.ReloadReport, ReloadCandidate, error)

type Service struct {
	manager  share.PipelineControl
	runtime  share.RuntimeControl
	loader   Loader
	reloadMu sync.Mutex
}

func New(manager share.PipelineControl, runtime share.RuntimeControl, loader Loader) *Service {
	return &Service{manager: manager, runtime: runtime, loader: loader}
}

func (s *Service) HookList() []share.Info {
	return s.manager.List()
}

func (s *Service) HookReload() (share.ReloadReport, error) {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	report, candidate, err := s.loader()
	if err != nil {
		return report, err
	}
	runtimeIDs := map[string]bool{}
	for _, info := range s.runtime.List() {
		runtimeIDs[info.ID] = true
	}
	for _, config := range candidate.Runtimes {
		runtimeIDs[config.ID] = true
	}
	ids := map[string]bool{}
	for _, info := range s.manager.List() {
		ids[info.PluginID] = true
	}
	for _, info := range candidate.Infos {
		ids[info.PluginID] = true
	}
	for id := range ids {
		if !runtimeIDs[id] {
			candidate.Commit(id)
		}
	}
	err = s.runtime.Apply(candidate.Runtimes, candidate.Commit)
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
	report, candidate, err := s.loader()
	s.reloadMu.Unlock()
	if err != nil {
		return nil, err
	}
	var config *share.RuntimeConfig
	for i := range candidate.Runtimes {
		if candidate.Runtimes[i].ID == id {
			value := candidate.Runtimes[i]
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
		return s.runtime.ReplacePlugin(id, config, func() { candidate.Commit(id) })
	}, nil
}

func (s *Service) StatefulHooks() []share.RuntimeInfo {
	return s.runtime.List()
}

func (s *Service) StartStatefulHook(id string) error {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	return s.runtime.Start(id)
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
