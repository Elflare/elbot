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
	"elbot/internal/signal"
	"elbot/internal/tool/builtin"
	"elbot/internal/tool/chat_history"
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
	fileRollback        *fileops.Service
}

// NewSupervisor 只装配组件；后台加载由 Start 启动，失败时返回已取得的总管供调用方关闭。
func NewSupervisor(ctx context.Context, cfg *config.Config, deps SupervisorDeps) (*Supervisor, error) {
	ctx, cancel := context.WithCancel(ctx)
	m := &Supervisor{ctx: ctx, cancel: cancel, done: make(chan struct{}), cronChanged: signal.New[CronChangedEvent, signal.Empty]("tool.cron_changed")}
	configDir := filepath.Dir(cfg.ConfigPath)
	dotEnv, err := config.LoadDotEnv(configDir)
	if err != nil {
		return m, fmt.Errorf("load tool environment: %w", err)
	}
	environment := processenv.New(os.Environ()).Fill(dotEnv)
	info := runtimeinfo.Info{
		ConfigDir:  configDir,
		ConfigPath: cfg.ConfigPath,
	}.Normalize()
	registry := toolregistry.NewRegistry(deps.Permission)
	residentStore := resident.NewStoreWithLimits(filepath.Join(configDir, "memories.toml"), resident.Limits{
		Core:   cfg.ResidentMemory.CoreMaxUnits,
		Normal: cfg.ResidentMemory.NormalMaxUnits,
	})
	mediaRuntime := toolmedia.NewMediaRuntime(deps.Media, cfg.Sandbox.Root)
	skillRoot := filepath.Join(configDir, "skills")
	skillManager := skill.NewManager(skillRoot, registry, mediaRuntime, environment)
	chatHistory := chat_history.NewManager(&chatHistoryBridge{platform: deps.Platform, media: mediaRuntime}, info)
	m.catalog = &runtime{
		registry:            registry,
		residentMemoryStore: residentStore,
		skillManager:        skillManager,
		fileRollback:        fileops.NewService(nil),
	}
	if err := registerAll(registry, registerOptions{
		media:               mediaRuntime,
		residentMemory:      residentMemoryBridge{store: residentStore},
		residentMemoryPath:  residentStore.Path,
		elwispCreatorConfig: configBridge{configPath: cfg.ConfigPath},
		skillManager:        skillManager,
		skillRoot:           skillRoot,
		chatHistory:         chatHistory,
		longMemoryDir:       filepath.Join(configDir, "long_memory"),
		fileRollback:        m.catalog.fileRollback,
		processEnv:          environment,
	}); err != nil {
		return m, fmt.Errorf("register tools: %w", err)
	}
	m.engine = execution.NewManager(registry, deps.Permission, mediaRuntime, logTool)
	m.preloader = preload.NewPreloadService(preload.PreloadOptions{
		Registry: registry,
		TagsPath: cfg.ToolTagsConfigPath,
		Tags:     cfg.ToolTags,
	})
	m.state = toolstate.NewStateService(deps.Store)
	cron := cronBridge{repo: deps.Store.CronJobs(), models: deps.Models, changed: m.cronChanged}
	m.releaseCronTools, err = registry.RegisterOwned(builtin.NewCronTools(cron, cron.ValidateSelection, info)...)
	if err != nil {
		return m, fmt.Errorf("register cron tools: %w", err)
	}
	return m, nil
}

// registerOptions 只传递注册内置工具所需的已构造组件与配置。
type registerOptions struct {
	residentMemory      builtin.ResidentMemoryService
	residentMemoryPath  string
	elwispCreatorConfig builtin.ElwispCreatorConfigProvider
	skillManager        *skill.Manager
	skillRoot           string                // 根部确定的技能资产目录，供文件保护规则使用。
	chatHistory         *chat_history.Manager // 已适配外部能力的聊天历史组件。
	longMemoryDir       string
	media               *toolmedia.MediaRuntime
	fileRollback        *fileops.Service
	processEnv          processenv.Environment
}

func registerAll(registry *toolregistry.Registry, opts registerOptions) error {
	mediaRuntime := opts.media
	if err := registry.Register(discovery.NewDiscoverTool(discovery.NewResolver(registry), opts.skillManager.EnsureLoaded)); err != nil {
		return err
	}
	if err := registry.Register(builtin.NewWorkspaceTool()); err != nil {
		return err
	}
	for _, memoryTool := range builtin.NewResidentMemoryTools(opts.residentMemory) {
		if err := registry.Register(memoryTool); err != nil {
			return err
		}
	}
	for _, memoryTool := range builtin.NewLongMemoryTools(opts.longMemoryDir) {
		if err := registry.Register(memoryTool); err != nil {
			return err
		}
	}
	if err := registry.Register(builtin.NewViewImageTool(opts.media, opts.chatHistory)); err != nil {
		return err
	}
	if err := registry.Register(builtin.NewSendFileTool(opts.media)); err != nil {
		return err
	}
	search := builtin.NewSearchChatHistoryTool(opts.chatHistory, opts.media)
	around := builtin.NewGetChatHistoryAroundTool(opts.chatHistory, opts.media)
	if err := registry.Register(builtin.NewGetMediaTool(opts.chatHistory)); err != nil {
		return err
	}
	if err := registry.Register(search); err != nil {
		return err
	}
	if err := registry.Register(around); err != nil {
		return err
	}
	if err := registry.Register(builtin.NewReplyToChatHistoryMessageTool(opts.chatHistory)); err != nil {
		return err
	}
	if err := registry.Register(builtin.NewWebSearchTool(opts.processEnv)); err != nil {
		return err
	}
	if err := registry.Register(builtin.NewWebExtractTool(opts.processEnv)); err != nil {
		return err
	}
	fileGuard := builtin.NewFileGuard(
		builtin.NewElSkillFileGuardRule(opts.skillRoot),
		builtin.NewResidentMemoryFileGuardRule(opts.residentMemoryPath),
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
	if err := registry.Register(builtin.NewElwispCreatorTool(opts.elwispCreatorConfig)); err != nil {
		return err
	}
	if err := registry.Register(skill.NewAgentSkillTool(opts.skillManager)); err != nil {
		return err
	}
	goRunner := skill.NewGoRunner(opts.skillManager)
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
