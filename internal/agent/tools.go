package agent

import (
	"elbot/internal/config"
	"elbot/internal/fileops"
	"elbot/internal/memory/resident"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
)

type toolRuntimeState struct {
	provider        ToolSchemaProvider
	manager         *toolrun.Manager
	registry        *tool.Registry
	fileRollback    *fileops.Service
	config          config.ToolsConfig
	defaultProvider bool
}

func buildPrompt(soulPath string, memory *resident.Store, provider ToolSchemaProvider, preloader *toolrun.PreloadService) PromptBuilder {
	soul := SoulProvider(staticSoulProvider{Prompt: "You are a helpful assistant."})
	if soulPath != "" {
		soul = &FileSoulProvider{Path: soulPath}
	}
	manager := NewSystemPromptManager(soulSystemPromptSource{Soul: soul})
	if nameProvider, ok := provider.(ToolNameProvider); ok {
		manager.AddSource(toolNamesSystemPromptSource{Tools: nameProvider})
	}
	if preloader != nil {
		manager.AddSource(toolTagsSystemPromptSource{Preloader: preloader})
	}
	manager.AddSource(residentMemorySystemPromptSource{Store: memory})
	manager.AddSource(conversationMetaSystemPromptSource{})
	return PromptBuilder{System: manager}
}
