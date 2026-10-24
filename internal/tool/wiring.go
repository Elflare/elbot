// 构造 Tool 内部组件，完成依赖注入和工具注册。
package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"elbot/internal/config"
	"elbot/internal/fileops"
	"elbot/internal/memory/resident"
	"elbot/internal/processenv"
	"elbot/internal/storage"
	"elbot/internal/tool/builtin"
	"elbot/internal/tool/discovery"
	"elbot/internal/tool/execution"
	toolmedia "elbot/internal/tool/media"
	"elbot/internal/tool/preload"
	toolregistry "elbot/internal/tool/registry"
	"elbot/internal/tool/runtimeinfo"
	"elbot/internal/tool/skill"
	toolstate "elbot/internal/tool/state"
)

// Runtime 保存装配出的组件，供总管转接能力，不承担执行流程。
type Runtime struct {
	Registry            *toolregistry.Registry
	ResidentMemoryStore *resident.Store
	SkillManager        *skill.Manager
	FileManager         *builtin.FileManager
	FileRollback        *fileops.Service
}

// NewSupervisor 只装配组件；后台加载由 Start 启动，失败时返回已取得的总管供调用方关闭。
func NewSupervisor(ctx context.Context, cfg *config.Config, deps SupervisorDeps) (*Supervisor, error) {
	ctx, cancel := context.WithCancel(ctx)
	m := &Supervisor{ctx: ctx, cancel: cancel, done: make(chan struct{})}
	configDir := filepath.Dir(cfg.ConfigPath)
	dotEnv, err := config.LoadDotEnv(configDir)
	if err != nil {
		return m, fmt.Errorf("load tool environment: %w", err)
	}
	environment := processenv.New(os.Environ()).Fill(dotEnv)
	info := runtimeinfo.Info{
		ConfigDir:   configDir,
		ConfigPath:  cfg.ConfigPath,
		SandboxRoot: cfg.Sandbox.Root,
	}.Normalize()
	registry := toolregistry.NewRegistry(deps.Permission)
	residentStore := resident.NewStoreWithLimits(filepath.Join(configDir, "memories.toml"), resident.Limits{
		Core:   cfg.ResidentMemory.CoreMaxUnits,
		Normal: cfg.ResidentMemory.NormalMaxUnits,
	})
	skillManager := skill.NewManager(filepath.Join(configDir, "skills"), registry, environment)
	fileManager := builtin.NewFileManager(info.SandboxRoot)
	fileManager.Media = deps.Media
	m.catalog = &Runtime{
		Registry:            registry,
		ResidentMemoryStore: residentStore,
		SkillManager:        skillManager,
		FileManager:         fileManager,
		FileRollback:        fileops.NewService(nil),
	}
	if err := RegisterAll(registry, RegisterOptions{
		RuntimeInfo:         info,
		ResidentMemoryStore: residentStore,
		SkillManager:        skillManager,
		ChatHistory:         deps.Platform,
		LongMemoryDir:       filepath.Join(configDir, "long_memory"),
		FileManager:         fileManager,
		FileRollback:        m.catalog.FileRollback,
		ProcessEnv:          environment,
	}); err != nil {
		return m, fmt.Errorf("register tools: %w", err)
	}
	m.engine = execution.NewManager(registry, deps.Permission)
	m.engine.Media = deps.Media
	m.engine.Log = logTool
	m.preloader = preload.NewPreloadService(preload.PreloadOptions{
		Registry:   registry,
		TagsPath:   cfg.ToolTagsConfigPath,
		Tags:       cfg.ToolTags,
		Permission: deps.Permission,
	})
	m.state = toolstate.NewStateService(deps.Store, registry)
	return m, nil
}

// RegisterOptions 只传递注册内置工具所需的已构造组件与配置。
type RegisterOptions struct {
	RuntimeInfo         runtimeinfo.Info
	ResidentMemoryStore *resident.Store
	SkillManager        *skill.Manager
	ChatHistory         interface {
		SearchChatHistory(context.Context, storage.ChatHistorySearchRequest) ([]storage.ChatMessage, error)
		ChatHistoryAround(context.Context, storage.ChatHistoryAroundRequest) ([]storage.ChatMessage, error)
		GetChatHistoryMessage(ctx context.Context, platform, scopeID, messageID string) (*storage.ChatMessage, error)
	} // 仅转交 Platform 的查询能力，不获取或拥有聊天历史存储。
	LongMemoryDir string
	FileManager   *builtin.FileManager
	FileRollback  *fileops.Service
	ProcessEnv    processenv.Environment
}

func RegisterAll(registry *toolregistry.Registry, opts RegisterOptions) error {
	info := opts.RuntimeInfo
	mediaRuntime := &toolmedia.MediaRuntime{Center: opts.FileManager.Media, SandboxRoot: info.SandboxRoot}
	opts.SkillManager.Scanner.Media = mediaRuntime
	if err := registry.Register(discovery.NewDiscoverTool(registry, opts.SkillManager.EnsureLoaded)); err != nil {
		return err
	}
	if err := registry.Register(builtin.NewWorkspaceTool()); err != nil {
		return err
	}
	for _, memoryTool := range builtin.NewResidentMemoryTools(opts.ResidentMemoryStore) {
		if err := registry.Register(memoryTool); err != nil {
			return err
		}
	}
	for _, memoryTool := range builtin.NewLongMemoryTools(opts.LongMemoryDir) {
		if err := registry.Register(memoryTool); err != nil {
			return err
		}
	}
	if err := registry.Register(builtin.NewViewImageTool(opts.FileManager.Media, opts.ChatHistory)); err != nil {
		return err
	}
	if err := registry.Register(builtin.NewSendFileTool(opts.FileManager)); err != nil {
		return err
	}
	search := builtin.NewSearchChatHistoryTool(opts.ChatHistory, opts.FileManager.Media, info)
	around := builtin.NewGetChatHistoryAroundTool(opts.ChatHistory, opts.FileManager.Media, info)
	if err := registry.Register(builtin.NewGetMediaTool(opts.ChatHistory, opts.FileManager.Media)); err != nil {
		return err
	}
	if err := registry.Register(search); err != nil {
		return err
	}
	if err := registry.Register(around); err != nil {
		return err
	}
	if err := registry.Register(builtin.NewReplyToChatHistoryMessageTool(opts.ChatHistory, info)); err != nil {
		return err
	}
	if err := registry.Register(builtin.NewWebSearchTool(opts.ProcessEnv)); err != nil {
		return err
	}
	if err := registry.Register(builtin.NewWebExtractTool(opts.ProcessEnv)); err != nil {
		return err
	}
	fileGuard := builtin.NewFileGuard(
		builtin.NewElSkillFileGuardRule(opts.SkillManager.Root),
		builtin.NewResidentMemoryFileGuardRule(opts.ResidentMemoryStore.Path),
		builtin.NewLongMemoryFileGuardRule(opts.LongMemoryDir),
	)
	opts.FileRollback.CheckWrite = fileGuard.CheckWrite
	editFile := builtin.NewEditFileTool(fileGuard)
	editFile.Rollback = opts.FileRollback
	if err := registry.Register(builtin.NewReadFileTool(fileGuard)); err != nil {
		return err
	}
	if err := registry.Register(editFile); err != nil {
		return err
	}
	if err := registry.Register(builtin.NewRollbackFileTool(opts.FileRollback)); err != nil {
		return err
	}
	shell := builtin.NewShellToolWithEnvironment(opts.ProcessEnv, fileGuard)
	shell.Media = mediaRuntime
	if err := registry.Register(shell); err != nil {
		return err
	}
	if err := registry.Register(builtin.NewElwispCreatorTool(info)); err != nil {
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
