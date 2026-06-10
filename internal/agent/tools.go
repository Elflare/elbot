package agent

import (
	"context"

	"elbot/internal/config"
	"elbot/internal/tool"
	"elbot/internal/tool/skill"
)

func (a *Agent) SetToolProvider(provider ToolSchemaProvider) {
	if provider == nil {
		provider = noopToolSchemaProvider{}
	}
	a.tools = provider
	if nameProvider, ok := provider.(ToolNameProvider); ok {
		a.promptBuilder.Tools = nameProvider
	} else {
		a.promptBuilder.Tools = noopToolSchemaProvider{}
	}
}

func (a *Agent) SetToolRuntime(registry *tool.Registry, scanner skill.Scanner) {
	a.toolRegistry = registry
	a.skillScanner = scanner
	if registry != nil {
		a.SetToolProvider(tool.SchemaProvider{Registry: registry, Policy: a.securityPolicy})
	}
}

func (a *Agent) SetToolConfig(cfg config.ToolsConfig) {
	if cfg.MaxRoundsPerTurn <= 0 {
		cfg.MaxRoundsPerTurn = 2
	}
	a.toolConfig = cfg
}

func (a *Agent) List() []tool.Info {
	if a.toolRegistry == nil {
		return nil
	}
	return a.toolRegistry.List()
}

func (a *Agent) Unregister(name string) error {
	if a.toolRegistry == nil {
		return nil
	}
	return a.toolRegistry.Unregister(name)
}

func (a *Agent) Remove(ctx context.Context, name string) error {
	if a.skillScanner == nil || a.toolRegistry == nil {
		return a.Unregister(name)
	}
	if remover, ok := a.skillScanner.(interface {
		Remove(context.Context, *tool.Registry, string) error
	}); ok {
		return remover.Remove(ctx, a.toolRegistry, name)
	}
	return a.Unregister(name)
}

func (a *Agent) Reload(ctx context.Context) error {
	if a.skillScanner == nil || a.toolRegistry == nil {
		return nil
	}
	return a.skillScanner.Reload(ctx, a.toolRegistry)
}
