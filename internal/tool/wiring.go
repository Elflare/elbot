// 构造 Tool 内部组件，完成依赖注入和工具注册。
package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"elbot/internal/config"
	"elbot/internal/fileops"
	"elbot/internal/memory/resident"
	"elbot/internal/processenv"
	"elbot/internal/signal"
	"elbot/internal/tool/builtin"
	"elbot/internal/tool/builtin/chat_history"
	"elbot/internal/tool/discovery"
	"elbot/internal/tool/execution"
	toolmedia "elbot/internal/tool/media"
	"elbot/internal/tool/preload"
	toolregistry "elbot/internal/tool/registry"
	"elbot/internal/tool/share"
	"elbot/internal/tool/skill"
	toolstate "elbot/internal/tool/state"
)

// NewSupervisor 只装配组件，失败时不返回实例；后台加载由 Start 启动。
func NewSupervisor(ctx context.Context, cfg *config.Config, deps SupervisorDeps) (*Supervisor, error) {
	configDir := filepath.Dir(cfg.ConfigPath)
	dotEnv, err := config.LoadDotEnv(configDir)
	if err != nil {
		return nil, fmt.Errorf("load tool environment: %w", err)
	}
	environment := processenv.New(os.Environ()).Fill(dotEnv)
	registry := toolregistry.NewRegistry(deps.Permission)
	resolver := discovery.NewResolver(registry)
	residentStore := resident.NewStoreWithLimits(filepath.Join(configDir, "memories.toml"), resident.Limits{
		Core:   cfg.ResidentMemory.CoreMaxUnits,
		Normal: cfg.ResidentMemory.NormalMaxUnits,
	})
	mediaRuntime := toolmedia.NewMediaRuntime(deps.Media, cfg.Sandbox.Root, deps.Media.PublishUsageFinished)
	skillRoot := filepath.Join(configDir, "skills")
	skillManager := skill.NewManager(skillRoot, registry, mediaRuntime, environment)
	fileRollback := fileops.NewService(nil)
	cronChanged := signal.New[CronChangedEvent, signal.Empty]("tool.cron_changed")
	cron := cronBridge{repo: deps.Store.CronJobs(), models: deps.Models, changed: cronChanged}
	history := chat_history.Deps{Repository: deps.ChatHistory, Media: mediaRuntime}
	if deps.Platform != nil {
		history.ResolveMedia = deps.Platform.ResolveMedia
	}
	definitions := builtin.NewTools(builtin.Deps{
		CreateQuestions:  deps.CreateQuestions,
		QuestionCommands: func() (string, string) { prefix := cfg.Commands.Prefixes[0]; return prefix + "a", prefix + "ac" },
		Media:            mediaRuntime, ResidentMemory: residentMemoryBridge{store: residentStore},
		ResidentMemoryPath: residentStore.Path, ElwispCreatorConfig: configBridge{configPath: cfg.ConfigPath},
		SkillRoot: skillRoot, ChatHistory: history, LongMemoryDir: filepath.Join(configDir, "long_memory"),
		FileRollback: fileRollback, ProcessEnv: environment,
		Cron: cron, ValidateModel: cron.ValidateSelection, Now: time.Now,
		ViewImageAvailable: func(ctx context.Context) bool {
			tools, err := registry.Query(ctx, share.ToolQuery{Names: []string{"view_image"}})
			return err == nil && len(tools) > 0
		},
	})
	definitions = append(definitions, skill.NewTools(skillManager)...)
	definitions = append(definitions, discovery.NewDiscoverTool(resolver))
	if _, err := registry.RegisterTools(ctx, share.SourceBuiltin, share.ToolRegistrationScope{}, definitions); err != nil {
		return nil, fmt.Errorf("register tools: %w", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	m := &Supervisor{
		registry: registry, resolver: resolver, residentMemoryStore: residentStore,
		skillManager: skillManager, fileRollback: fileRollback, cronChanged: cronChanged,
		ctx: ctx, cancel: cancel, done: make(chan struct{}),
	}
	m.engine = execution.NewManager(execution.ManagerDeps{RecordToolCall: deps.Store.ToolCalls().Create, Registry: registry, Permission: deps.Permission, Media: mediaRuntime, Log: logTool, InfoAvailableInContext: toolregistry.InfoAvailableInContext, UnavailableReason: toolregistry.UnavailableReason, BackgroundToolAllowed: toolregistry.BackgroundToolAllowed, DecodeState: toolstate.DecodeState, PrepareUpdate: toolstate.PrepareUpdate, DiscoveryStateUpdate: resolver.DiscoveryStateUpdate})
	m.preloader = preload.NewPreloadService(preload.PreloadOptions{
		RegisterTools: registry.RegisterTools,
		Resolver:      resolver, NativeDiscoveredToolsFromDiscovery: discovery.NativeDiscoveredToolsFromDiscovery, BackgroundDiscoveredTools: toolregistry.BackgroundDiscoveredTools,
		Registry: registry,
		TagsPath: cfg.ToolTagsConfigPath,
		Tags:     cfg.ToolTags,
	})
	m.state = toolstate.NewStateService(deps.Store.Sessions(), toolregistry.BackgroundDiscoveredTools)
	return m, nil
}
