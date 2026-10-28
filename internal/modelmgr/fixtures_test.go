package modelmgr

import (
	"context"
	"elbot/internal/config"
	"elbot/internal/llm"
	"testing"
)

// Unit tests inject only the operation under test into an already constructed manager.
type testModelOptions struct {
	Clients                   map[string]llm.Client
	Providers                 map[string]config.ProviderConfig
	ModeModels                map[string]config.ModelSelection
	CompactModel, NamingModel config.ModelSelection
	StatePath, DefaultMode    string
	Metadata                  config.ModelMetadataConfig
}

func buildTestManager(t *testing.T, opts testModelOptions) (*ModelManager, error) {
	t.Helper()
	// 夹具绕过配置加载，显式补齐配置层提供的窗口默认值。
	if opts.Metadata.DefaultContextWindow == 0 {
		opts.Metadata.DefaultContextWindow = config.DefaultContextWindow
	}
	s, err := NewModelManager(t.Context(), &config.Config{
		Providers: opts.Providers, ModeModels: opts.ModeModels,
		CompactModel: opts.CompactModel, NamingModel: opts.NamingModel,
		StateConfigPath: opts.StatePath, Session: config.SessionConfig{DefaultMode: opts.DefaultMode},
		ModelMetadata: opts.Metadata,
	})
	if s != nil {
		t.Cleanup(func() {
			if err := s.Close(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	if err != nil {
		return s, err
	}
	for name, client := range opts.Clients {
		s.providers.clients[name] = client
		s.bindRetryNotifier(name, client)
	}
	return s, nil
}

func selectTestModel(s *ModelManager, mode, arg string) (ModelOption, error) {
	selected, err := s.PrepareModel(arg)
	if err != nil {
		return ModelOption{}, err
	}
	return s.CommitModelForMode(mode, selected)
}
