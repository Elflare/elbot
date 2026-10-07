package agent

import (
	"elbot/internal/agent/dialogue"
	"elbot/internal/fileops"
	"elbot/internal/memory/resident"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
)

type toolRuntimeState struct {
	provider        dialogue.ToolSchemaProvider
	manager         *toolrun.Manager
	registry        *tool.Registry
	fileRollback    *fileops.Service
	defaultProvider bool
}

func buildSystemPrompt(soulPath string, memory *resident.Store, provider dialogue.ToolSchemaProvider, preloader *toolrun.PreloadService) dialogue.SystemPromptManager {
	soul := dialogue.SoulProvider(dialogue.StaticSoulProvider{Prompt: "You are a helpful assistant."})
	if soulPath != "" {
		soul = &dialogue.FileSoulProvider{Path: soulPath}
	}
	manager := dialogue.NewSystemPromptManager(dialogue.SoulSystemPromptSource{Soul: soul})
	if nameProvider, ok := provider.(dialogue.ToolNameProvider); ok {
		manager.AddSource(dialogue.ToolNamesSystemPromptSource{Tools: nameProvider})
	}
	if preloader != nil {
		manager.AddSource(dialogue.ToolTagsSystemPromptSource{Preloader: preloader})
	}
	manager.AddSource(dialogue.BackgroundPathsSystemPromptSource{})
	manager.AddSource(dialogue.ResidentMemorySystemPromptSource{Store: memory})
	manager.AddSource(dialogue.ConversationMetaSystemPromptSource{})
	return manager
}
