// Package modelmgr owns model selection, provider clients and model catalogs.
package modelmgr

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"elbot/internal/config"
	"elbot/internal/signal"
)

// ModelManager publishes complete selection snapshots. Reloading replaces provider
// data as a unit; callers retain their snapshot for the lifetime of an operation.
type ModelManager struct {
	mu             sync.RWMutex
	state          runtimeState
	commitMu       sync.Mutex
	statePath      string
	sessionState   config.StateSessionConfig
	save           func(string, config.StateConfig) error
	providers      *providerSet
	reloadMu       sync.Mutex
	providersPath  string                  // 重载读取的 providers 配置文件。
	configDir      string                  // 主配置目录，用于解析重载凭据。
	request        config.LLMRequestConfig // 构造时固定的请求参数，providers 重载不修改它。
	retrying       *signal.Signal[ModelRetryingEvent, signal.Empty]
	logConnections []*signal.Connection
}

// NewModelManager 接收已加载的完整配置；部分构造结果由 App 统一关闭。
func NewModelManager(ctx context.Context, cfg *config.Config) (*ModelManager, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// 配置层已校验必需槽；这里只检查使用模型所需的厂商引用。
	for mode, selected := range cfg.ModeModels {
		if selected.Provider == "" || selected.Model == "" {
			continue // 未配置完整的可选槽按 ResolveMode 回退到 work。
		}
		if err := validateSelection("mode_models."+mode, selected, cfg.Providers); err != nil {
			return nil, err
		}
	}
	for name, selected := range map[string]config.ModelSelection{"compact_model": cfg.CompactModel, "naming_model": cfg.NamingModel} {
		if selected.Provider == "" && selected.Model == "" {
			continue
		}
		if err := validateSelection(name, selected, cfg.Providers); err != nil {
			return nil, err
		}
	}
	s := &ModelManager{
		state:         runtimeState{modes: cloneModes(cfg.ModeModels), compact: cfg.CompactModel, naming: cfg.NamingModel, listProvider: cfg.ModeModels["work"].Provider},
		statePath:     cfg.StateConfigPath,
		sessionState:  config.StateSessionConfig{DefaultMode: cfg.Session.DefaultMode},
		save:          config.SaveState,
		providersPath: cfg.ProvidersConfigPath,
		configDir:     filepath.Dir(cfg.ConfigPath),
		request:       cfg.LLMRequest,
		retrying:      signal.New[ModelRetryingEvent, signal.Empty]("model.retrying"),
	}
	var err error
	s.providers, err = s.newProviderSet(ctx, config.ProviderFile{Providers: cfg.Providers, ModelMetadata: cfg.ModelMetadata})
	if err != nil {
		return s, err
	}
	if err := s.connectLogSignals(); err != nil {
		return s, err
	}
	return s, nil
}

func validateSelection(name string, selected config.ModelSelection, providers map[string]config.ProviderConfig) error {
	if selected.Provider == "" || selected.Model == "" {
		return fmt.Errorf("%s provider/model must both be set", name)
	}
	if _, ok := providers[selected.Provider]; !ok {
		return fmt.Errorf("%s provider %q not found", name, selected.Provider)
	}
	return nil
}

// Close 在消费者退出后关闭信号并等待订阅收尾；超时可以再次等待。
func (s *ModelManager) Close(ctx context.Context) error {
	if err := s.retrying.Close(ctx); err != nil {
		return err
	}
	for _, connection := range s.logConnections {
		if err := connection.Close(ctx); err != nil {
			return err
		}
	}
	return nil
}
