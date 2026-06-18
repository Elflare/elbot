package agent

import (
	"context"

	"elbot/internal/config"
	"elbot/internal/tool"
	"elbot/internal/tool/skill"
	"elbot/internal/toolrun"
)

type toolRuntimeState struct {
	provider        ToolSchemaProvider
	manager         *toolrun.Manager
	registry        *tool.Registry
	scanner         skill.Scanner
	config          config.ToolsConfig
	toolTags        *toolTagConfigSource
	defaultProvider bool
}

func newToolRuntimeState() toolRuntimeState {
	return toolRuntimeState{
		provider: noopToolSchemaProvider{},
		config:   config.Default().Tools,
	}
}

func (a *Agent) rebuildSystemPrompt() {
	manager := NewSystemPromptManager(soulSystemPromptSource{Soul: a.soul})
	if nameProvider, ok := a.toolRuntime.provider.(ToolNameProvider); ok {
		manager.AddSource(toolNamesSystemPromptSource{Tools: nameProvider})
	}
	if a.toolRuntime.toolTags != nil {
		manager.AddSource(a.toolRuntime.toolTags)
	}
	a.promptBuilder.System = manager
}

func (a *Agent) SetToolProvider(provider ToolSchemaProvider) {
	if provider == nil {
		provider = noopToolSchemaProvider{}
	}
	a.toolRuntime.provider = provider
	a.toolRuntime.defaultProvider = false
	a.rebuildSystemPrompt()
}

func (a *Agent) SetToolRuntime(registry *tool.Registry, scanner skill.Scanner) {
	a.toolRuntime.registry = registry
	a.toolRuntime.scanner = scanner
	a.toolRuntime.manager = toolrun.NewManager(registry, a.securityPolicy)
	if registry != nil {
		a.toolRuntime.provider = toolRunPromptProvider{agent: a}
		a.toolRuntime.defaultProvider = true
	}
	a.rebuildSystemPrompt()
}

func (a *Agent) SetToolConfig(cfg config.ToolsConfig) {
	if cfg.MaxRoundsPerTurn <= 0 {
		cfg.MaxRoundsPerTurn = 2
	}
	a.toolRuntime.config = cfg
}

func (a *Agent) SetToolTagConfig(path string, cfg config.ToolTagsConfig) {
	a.toolRuntime.toolTags = newToolTagConfigSource(path, cfg)
	a.rebuildSystemPrompt()
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
