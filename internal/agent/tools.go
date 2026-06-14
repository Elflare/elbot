package agent

import (
	"context"

	"elbot/internal/config"
	"elbot/internal/tool"
	"elbot/internal/tool/skill"
)

type toolRuntimeState struct {
	provider ToolSchemaProvider
	registry *tool.Registry
	scanner  skill.Scanner
	config   config.ToolsConfig
}

func newToolRuntimeState() toolRuntimeState {
	return toolRuntimeState{
		provider: noopToolSchemaProvider{},
		config:   config.Default().Tools,
	}
}

func (a *Agent) SetToolProvider(provider ToolSchemaProvider) {
	if provider == nil {
		provider = noopToolSchemaProvider{}
	}
	a.toolRuntime.provider = provider
	if nameProvider, ok := provider.(ToolNameProvider); ok {
		a.promptBuilder.Tools = nameProvider
	} else {
		a.promptBuilder.Tools = noopToolSchemaProvider{}
	}
}

func (a *Agent) SetToolRuntime(registry *tool.Registry, scanner skill.Scanner) {
	a.toolRuntime.registry = registry
	a.toolRuntime.scanner = scanner
	if registry != nil {
		a.SetToolProvider(tool.SchemaProvider{Registry: registry, Policy: a.securityPolicy})
	}
}

func (a *Agent) SetToolConfig(cfg config.ToolsConfig) {
	if cfg.MaxRoundsPerTurn <= 0 {
		cfg.MaxRoundsPerTurn = 2
	}
	a.toolRuntime.config = cfg
}

func (a *Agent) List() []tool.Info {
	if a.toolRuntime.registry == nil {
		return nil
	}
	return a.toolRuntime.registry.List()
}

func (a *Agent) Unregister(name string) error {
	if a.toolRuntime.registry == nil {
		return nil
	}
	return a.toolRuntime.registry.Unregister(name)
}

func (a *Agent) Remove(ctx context.Context, name string) error {
	if a.toolRuntime.scanner == nil || a.toolRuntime.registry == nil {
		return a.Unregister(name)
	}
	if remover, ok := a.toolRuntime.scanner.(interface {
		Remove(context.Context, *tool.Registry, string) error
	}); ok {
		return remover.Remove(ctx, a.toolRuntime.registry, name)
	}
	return a.Unregister(name)
}

func (a *Agent) Reload(ctx context.Context) error {
	if a.toolRuntime.scanner == nil || a.toolRuntime.registry == nil {
		return nil
	}
	return a.toolRuntime.scanner.Reload(ctx, a.toolRuntime.registry)
}
