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

// runtime 保存装配出的组件，供总管转接能力，不承担执行流程。
type runtime struct {
	registry            *toolregistry.Registry
	residentMemoryStore *resident.Store
	skillManager        *skill.Manager
	fileManager         *builtin.FileManager
	fileRollback        *fileops.Service
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
	m.catalog = &runtime{
		registry:            registry,
		residentMemoryStore: residentStore,
		skillManager:        skillManager,
		fileManager:         fileManager,
		fileRollback:        fileops.NewService(nil),
	}
	if err := registerAll(registry, registerOptions{
		runtimeInfo:         info,
		residentMemoryStore: residentStore,
		skillManager:        skillManager,
		chatHistory:         deps.Platform,
		longMemoryDir:       filepath.Join(configDir, "long_memory"),
		fileManager:         fileManager,
		fileRollback:        m.catalog.fileRollback,
		processEnv:          environment,
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
	m.state = toolstate.NewStateService(deps.Store)
	return m, nil
}

// registerOptions 只传递注册内置工具所需的已构造组件与配置。
type registerOptions struct {
	runtimeInfo         runtimeinfo.Info
	residentMemoryStore *resident.Store
	skillManager        *skill.Manager
	chatHistory         interface {
		SearchChatHistory(context.Context, storage.ChatHistorySearchRequest) ([]storage.ChatMessage, error)
		ChatHistoryAround(context.Context, storage.ChatHistoryAroundRequest) ([]storage.ChatMessage, error)
		GetChatHistoryMessage(ctx context.Context, platform, scopeID, messageID string) (*storage.ChatMessage, error)
	} // 仅转交 Platform 的查询能力，不获取或拥有聊天历史存储。
	longMemoryDir string
	fileManager   *builtin.FileManager
	fileRollback  *fileops.Service
	processEnv    processenv.Environment
}

func registerAll(registry *toolregistry.Registry, opts registerOptions) error {
	info := opts.runtimeInfo
	mediaRuntime := &toolmedia.MediaRuntime{Center: opts.fileManager.Media, SandboxRoot: info.SandboxRoot}
	opts.skillManager.Scanner.Media = mediaRuntime
	if err := registry.Register(discovery.NewDiscoverTool(registry, opts.skillManager.EnsureLoaded)); err != nil {
		return err
	}
	if err := registry.Register(builtin.NewWorkspaceTool()); err != nil {
		return err
	}
	for _, memoryTool := range builtin.NewResidentMemoryTools(opts.residentMemoryStore) {
		if err := registry.Register(memoryTool); err != nil {
			return err
		}
	}
	for _, memoryTool := range builtin.NewLongMemoryTools(opts.longMemoryDir) {
		if err := registry.Register(memoryTool); err != nil {
			return err
		}
	}
	if err := registry.Register(builtin.NewViewImageTool(opts.fileManager.Media, opts.chatHistory)); err != nil {
		return err
	}
	if err := registry.Register(builtin.NewSendFileTool(opts.fileManager)); err != nil {
		return err
	}
	search := builtin.NewSearchChatHistoryTool(opts.chatHistory, opts.fileManager.Media, info)
	around := builtin.NewGetChatHistoryAroundTool(opts.chatHistory, opts.fileManager.Media, info)
	if err := registry.Register(builtin.NewGetMediaTool(opts.chatHistory, opts.fileManager.Media)); err != nil {
		return err
	}
	if err := registry.Register(search); err != nil {
		return err
	}
	if err := registry.Register(around); err != nil {
		return err
	}
	if err := registry.Register(builtin.NewReplyToChatHistoryMessageTool(opts.chatHistory, info)); err != nil {
		return err
	}
	if err := registry.Register(builtin.NewWebSearchTool(opts.processEnv)); err != nil {
		return err
	}
	if err := registry.Register(builtin.NewWebExtractTool(opts.processEnv)); err != nil {
		return err
	}
	fileGuard := builtin.NewFileGuard(
		builtin.NewElSkillFileGuardRule(opts.skillManager.Root),
		builtin.NewResidentMemoryFileGuardRule(opts.residentMemoryStore.Path),
		builtin.NewLongMemoryFileGuardRule(opts.longMemoryDir),
	)
	opts.fileRollback.CheckWrite = fileGuard.CheckWrite
	editFile := builtin.NewEditFileTool(fileGuard)
	editFile.Rollback = opts.fileRollback
	if err := registry.Register(builtin.NewReadFileTool(fileGuard)); err != nil {
		return err
	}
	if err := registry.Register(editFile); err != nil {
		return err
	}
	if err := registry.Register(builtin.NewRollbackFileTool(opts.fileRollback)); err != nil {
		return err
	}
	shell := builtin.NewShellToolWithEnvironment(opts.processEnv, fileGuard)
	shell.Media = mediaRuntime
	if err := registry.Register(shell); err != nil {
		return err
	}
	if err := registry.Register(builtin.NewElwispCreatorTool(info)); err != nil {
		return err
	}
	if err := registry.Register(skill.NewAgentSkillTool(opts.skillManager)); err != nil {
		return err
	}
	goRunner := skill.NewGoRunner(opts.skillManager.Catalog, opts.processEnv)
	goRunner.Media = mediaRuntime
	if err := registry.Register(goRunner); err != nil {
		return err
	}
	if err := registry.Register(skill.NewCreateElSkillTool(opts.skillManager)); err != nil {
		return err
	}
	if err := registry.Register(skill.NewReadElSkillTool(opts.skillManager)); err != nil {
		return err
	}
	if err := registry.Register(skill.NewModifyElSkillTool(opts.skillManager)); err != nil {
		return err
	}
	if err := registry.Register(skill.NewFinalizeElSkillTool(opts.skillManager)); err != nil {
		return err
	}
	return nil
}
