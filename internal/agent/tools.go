package agent

import (
	"elbot/internal/config"
	"elbot/internal/fileops"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
)

type toolRuntimeState struct {
	provider        ToolSchemaProvider
	manager         *toolrun.Manager
	registry        *tool.Registry
	fileRollback    *fileops.Service
	config          config.ToolsConfig
	toolTags        *toolTagConfigSource
	defaultProvider bool
}

func newToolRuntimeState() toolRuntimeState {
	return toolRuntimeState{
		provider:        noopToolSchemaProvider{},
		defaultProvider: true,
		config:          config.Default().Tools,
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
	manager.AddSource(residentMemorySystemPromptSource{Store: a.residentMemory})
	manager.AddSource(conversationMetaSystemPromptSource{})
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
