// 协调 Hook 查询、重载、启动、停止、重启和候选配置提交。

package control

import (
	"context"
	"elbot/internal/operationresult"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
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
	if err != nil { // 总配置未通过校验，loader 已报告；没有可提交的候选。
		return report, nil
	}
	next := map[string]*share.RuntimeConfig{}
	for _, info := range append(s.manager.List(), candidate.Infos...) {
		next[info.PluginID] = nil
	}
	for _, info := range s.runtime.List() {
		next[info.ID] = nil
	}
	for i := range candidate.Runtimes {
		config := &candidate.Runtimes[i]
		next[config.ID] = config
	}
	for _, id := range report.FailedIDs {
		delete(next, id)
	}
	for _, id := range slices.Sorted(maps.Keys(next)) {
		err := s.runtime.ReplacePlugin(id, next[id], func() { candidate.Commit(id) })
		if errors.Is(err, share.ErrClosed) {
			return report, err
		}
		if err != nil {
			report.Notices = append(report.Notices, err.Error())
			events.Notify(context.Background(), events.Notification{Text: err.Error(), Level: slog.LevelWarn})
		}
	}
	return report, nil
}

// PreparePluginReload validates a complete candidate snapshot immediately and
// returns a commit that replaces only the requesting persistent plugin.
// The commit returns the original reload error.
func (s *Service) PreparePluginReload(id string) (func() error, error) {
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
			config = &candidate.Runtimes[i]
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
		if err := s.runtime.ValidatePlugin(*config); err != nil {
			return err
		}
		return s.runtime.ReplacePlugin(id, config, func() { candidate.Commit(id) })
	}, nil
}

func (s *Service) StatefulHooks() []share.RuntimeInfo {
	return s.runtime.List()
}

func (s *Service) StartStatefulHook(id string) (operationresult.Result[struct{}], error) {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	return s.runtime.Start(id)
}

func (s *Service) StopHook(ctx context.Context, id string) (operationresult.Result[struct{}], error) {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	matched := s.manager.Stop(id)
	runtimeStopped, err := s.runtime.Stop(ctx, id)
	if err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	if runtimeStopped.Status == operationresult.NotFound && matched {
		return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
	}
	return runtimeStopped, nil
}

func (s *Service) RestartStatefulHook(ctx context.Context, id string) (operationresult.Result[struct{}], error) {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	return s.runtime.Restart(ctx, id)
}
