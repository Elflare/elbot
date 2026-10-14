package builtin

import (
	"elbot/internal/fileops"
	"elbot/internal/memory/resident"
	"elbot/internal/processenv"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/tool/runtimeinfo"
	"elbot/internal/tool/skill"
)

type RegisterOptions struct {
	RuntimeInfo         runtimeinfo.Info
	ResidentMemoryStore *resident.Store
	SkillManager        *skill.Manager
	ChatHistory         storage.ChatHistoryRepository
	LongMemoryDir       string
	FileManager         *FileManager
	FileRollback        *fileops.Service
	ProcessEnv          processenv.Environment
}

func RegisterAll(registry *tool.Registry, opts RegisterOptions) error {
	info := opts.RuntimeInfo
	mediaRuntime := &tool.MediaRuntime{Center: opts.FileManager.Media, SandboxRoot: info.SandboxRoot}
	opts.SkillManager.Scanner.Media = mediaRuntime
	if err := registry.Register(tool.NewDiscoverTool(registry, opts.SkillManager.EnsureLoaded)); err != nil {
		return err
	}
	if err := registry.Register(NewWorkspaceTool()); err != nil {
		return err
	}
	for _, memoryTool := range NewResidentMemoryTools(opts.ResidentMemoryStore) {
		if err := registry.Register(memoryTool); err != nil {
			return err
		}
	}
	for _, memoryTool := range NewLongMemoryTools(opts.LongMemoryDir) {
		if err := registry.Register(memoryTool); err != nil {
			return err
		}
	}
	if err := registry.Register(NewViewImageTool(opts.FileManager.Media, opts.ChatHistory)); err != nil {
		return err
	}
	if err := registry.Register(NewSendFileTool(opts.FileManager)); err != nil {
		return err
	}
	search := NewSearchChatHistoryTool(opts.ChatHistory, info)
	around := NewGetChatHistoryAroundTool(opts.ChatHistory, info)
	search.center, around.center = opts.FileManager.Media, opts.FileManager.Media
	if err := registry.Register(NewGetMediaTool(opts.ChatHistory, search.center)); err != nil {
		return err
	}
	if err := registry.Register(search); err != nil {
		return err
	}
	if err := registry.Register(around); err != nil {
		return err
	}
	if err := registry.Register(NewReplyToChatHistoryMessageTool(opts.ChatHistory, info)); err != nil {
		return err
	}
	if err := registry.Register(NewWebSearchTool(opts.ProcessEnv)); err != nil {
		return err
	}
	if err := registry.Register(NewWebExtractTool(opts.ProcessEnv)); err != nil {
		return err
	}
	fileGuard := NewFileGuard(
		NewElSkillFileGuardRule(opts.SkillManager.Root),
		NewResidentMemoryFileGuardRule(opts.ResidentMemoryStore.Path),
		NewLongMemoryFileGuardRule(opts.LongMemoryDir),
	)
	opts.FileRollback.CheckWrite = fileGuard.CheckWrite
	editFile := NewEditFileTool(fileGuard)
	editFile.Rollback = opts.FileRollback
	if err := registry.Register(NewReadFileTool(fileGuard)); err != nil {
		return err
	}
	if err := registry.Register(editFile); err != nil {
		return err
	}
	if err := registry.Register(NewRollbackFileTool(opts.FileRollback)); err != nil {
		return err
	}
	shell := NewShellToolWithEnvironment(opts.ProcessEnv, fileGuard)
	shell.Media = mediaRuntime
	if err := registry.Register(shell); err != nil {
		return err
	}
	if err := registry.Register(NewElwispCreatorTool(info)); err != nil {
		return err
	}
	if err := registry.Register(skill.NewAgentSkillTool(opts.SkillManager)); err != nil {
		return err
	}
	goRunner := skill.NewGoRunner(opts.SkillManager.Catalog, opts.ProcessEnv)
	goRunner.Media = mediaRuntime
	if err := registry.Register(goRunner); err != nil {
		return err
	}
	if err := registry.Register(skill.NewCreateElSkillTool(opts.SkillManager)); err != nil {
		return err
	}
	if err := registry.Register(skill.NewReadElSkillTool(opts.SkillManager)); err != nil {
		return err
	}
	if err := registry.Register(skill.NewModifyElSkillTool(opts.SkillManager)); err != nil {
		return err
	}
	if err := registry.Register(skill.NewFinalizeElSkillTool(opts.SkillManager)); err != nil {
		return err
	}
	return nil
}
