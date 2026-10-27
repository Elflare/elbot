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
	"elbot/internal/llm"
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
	"elbot/internal/tool/share"
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
	registry := toolregistry.NewRegistry(deps.Permission)
	residentStore := resident.NewStoreWithLimits(filepath.Join(configDir, "memories.toml"), resident.Limits{
		Core:   cfg.ResidentMemory.CoreMaxUnits,
		Normal: cfg.ResidentMemory.NormalMaxUnits,
	})
	mediaRuntime := toolmedia.NewMediaRuntime(deps.Media, cfg.Sandbox.Root)
	skillRoot := filepath.Join(configDir, "skills")
	skillManager := skill.NewManager(skillRoot, registry, mediaRuntime, environment)
	chatHistory := chat_history.NewManager(&chatHistoryBridge{history: deps.ChatHistory, platform: deps.Platform, media: mediaRuntime}, time.Now)
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
	m.engine = execution.NewManager(execution.ManagerDeps{Registry: registry, Permission: deps.Permission, Media: mediaRuntime, Log: logTool, InfoAvailableInContext: toolregistry.InfoAvailableInContext, UnavailableReason: toolregistry.UnavailableReason, BackgroundDiscoveredTools: toolregistry.BackgroundDiscoveredTools, DecodeState: toolstate.DecodeState, PrepareUpdate: toolstate.PrepareUpdate, DiscoveryStateUpdate: discovery.NewResolver(registry).DiscoveryStateUpdate})
	m.preloader = preload.NewPreloadService(preload.PreloadOptions{
		Resolver: discovery.NewResolver(registry), NativeDiscoveredToolsFromDiscovery: discovery.NativeDiscoveredToolsFromDiscovery, LoadDiscoveryContent: discovery.LoadDiscoveryContent, BackgroundDiscoveredTools: toolregistry.BackgroundDiscoveredTools,
		Registry: registry,
		TagsPath: cfg.ToolTagsConfigPath,
		Tags:     cfg.ToolTags,
	})
	m.state = toolstate.NewStateService(deps.Store.Sessions(), toolregistry.BackgroundDiscoveredTools)
	cron := cronBridge{repo: deps.Store.CronJobs(), models: deps.Models, changed: m.cronChanged}
	m.releaseCronTools, err = registry.RegisterOwned(builtin.NewCronTools(cron, cron.ValidateSelection, time.Now)...)
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
	viewImageAvailable := func(ctx context.Context) bool {
		tools, err := registry.QueryTools(ctx, share.ToolQuery{Names: []string{"view_image"}})
		return err == nil && len(tools) > 0
	}
	search := builtin.NewSearchChatHistoryTool(opts.chatHistory, viewImageAvailable)
	around := builtin.NewGetChatHistoryAroundTool(opts.chatHistory, viewImageAvailable)
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

// executionBridge 将本次调用借用的公开回调转换成执行器的内部契约。
type executionBridge struct{ RunnerDeps }

func (b executionBridge) ConfirmToolCall(ctx context.Context, id string, call llm.ToolCallRequest, assessment share.RiskAssessment, detail string) (share.ConfirmResult, error) {
	result, err := b.RunnerDeps.ConfirmToolCall(ctx, id, call, RiskAssessment(assessment), detail)
	return share.ConfirmResult(result), err
}

func toInternalToolQuery(query ToolQuery) share.ToolQuery { return share.ToolQuery(query) }

func toPublicInfo(info share.Info) Info {
	return Info{NeedsElyphRules: info.NeedsElyphRules, APITypes: info.APITypes, Name: info.Name,
		Description: info.Description, Source: Source(info.Source), Risk: info.Risk,
		SuperadminOnly: info.SuperadminOnly, Hidden: info.Hidden, OwnerScoped: info.OwnerScoped,
		ForegroundOnly: info.ForegroundOnly, Tags: info.Tags, DependsOn: info.DependsOn}
}

func toPublicInfos(infos []share.Info) []Info {
	if infos == nil {
		return nil
	}
	out := make([]Info, len(infos))
	for i, info := range infos {
		out[i] = toPublicInfo(info)
	}
	return out
}

func toPublicDiscoveredTools(items []share.DiscoveredTool) []DiscoveredTool {
	if items == nil {
		return nil
	}
	out := make([]DiscoveredTool, len(items))
	for i, item := range items {
		out[i] = DiscoveredTool{Name: item.Name, CanonicalName: item.CanonicalName,
			Source: SourceKind(item.Source), Description: item.Description, Schema: item.Schema,
			EventKey: item.EventKey, Endpoint: item.Endpoint, TimeoutSeconds: item.TimeoutSeconds,
			ForegroundOnly: item.ForegroundOnly}
	}
	return out
}

func toInternalDiscoveredTools(items []DiscoveredTool) []share.DiscoveredTool {
	if items == nil {
		return nil
	}
	out := make([]share.DiscoveredTool, len(items))
	for i, item := range items {
		out[i] = share.DiscoveredTool{Name: item.Name, CanonicalName: item.CanonicalName,
			Source: share.SourceKind(item.Source), Description: item.Description, Schema: item.Schema,
			EventKey: item.EventKey, Endpoint: item.Endpoint, TimeoutSeconds: item.TimeoutSeconds,
			ForegroundOnly: item.ForegroundOnly}
	}
	return out
}

func toPublicState(state share.State) State {
	return State{DiscoveredTools: toPublicDiscoveredTools(state.DiscoveredTools), ToolTags: state.ToolTags, ElyphRulesShown: state.ElyphRulesShown}
}

func toInternalContext(view Context) share.Context { return share.Context(view) }

func toInternalStateUpdate(update StateUpdate) share.StateUpdate {
	return share.StateUpdate{Tools: toInternalDiscoveredTools(update.Tools), Tags: update.Tags}
}

func toPublicStateUpdate(update share.StateUpdate) StateUpdate {
	return StateUpdate{Tools: toPublicDiscoveredTools(update.Tools), Tags: update.Tags}
}

func toPublicStateCommit(committed share.StateCommit) StateCommit {
	return StateCommit{State: toPublicState(committed.State), Metadata: committed.Metadata, Injected: committed.Injected, Existing: committed.Existing}
}

func toPublicToolPreload(prepared share.ToolPreload) ToolPreload {
	return ToolPreload{NeedsElyphRules: prepared.NeedsElyphRules, Update: toPublicStateUpdate(prepared.Update), Accepted: prepared.Accepted, Invalid: prepared.Invalid, Content: prepared.Content}
}

func toPublicSkillPreload(prepared share.SkillPreload) SkillPreload {
	return SkillPreload{NeedsElyphRules: prepared.NeedsElyphRules, Update: toPublicStateUpdate(prepared.Update), Accepted: prepared.Accepted, Names: prepared.Names, Invalid: prepared.Invalid, Content: prepared.Content}
}

func toInternalBackgroundPreloadRequest(req BackgroundPreloadRequest) share.BackgroundPreloadRequest {
	var declarations []share.ELwispToolDeclaration
	if req.ELwispTools != nil {
		declarations = make([]share.ELwispToolDeclaration, len(req.ELwispTools))
		for i, declaration := range req.ELwispTools {
			declarations[i] = share.ELwispToolDeclaration(declaration)
		}
	}
	return share.BackgroundPreloadRequest{SessionID: req.SessionID, Names: req.Names, AllowedToolNames: req.AllowedToolNames, ELwispTools: declarations}
}

func toPublicBackgroundPreload(prepared share.BackgroundPreload) BackgroundPreload {
	return BackgroundPreload{NeedsElyphRules: prepared.NeedsElyphRules, Update: toPublicStateUpdate(prepared.Update), Skills: prepared.Skills, SkillPrompt: prepared.SkillPrompt}
}

func toPublicBackgroundSelections(items []share.BackgroundSelection) []BackgroundSelection {
	if items == nil {
		return nil
	}
	out := make([]BackgroundSelection, len(items))
	for i, item := range items {
		out[i] = BackgroundSelection(item)
	}
	return out
}

func toPublicTagPrompts(items []share.TagPrompt) []TagPrompt {
	if items == nil {
		return nil
	}
	out := make([]TagPrompt, len(items))
	for i, item := range items {
		out[i] = TagPrompt(item)
	}
	return out
}

func toInternalRunRequest(req RunRequest) share.RunRequest {
	return share.RunRequest{Committer: req.Committer, Session: req.Session, Calls: req.Calls, AssistantText: req.AssistantText, AssistantRawText: req.AssistantRawText, DiscoveredTools: toInternalDiscoveredTools(req.DiscoveredTools), Actor: req.Actor}
}

func toPublicRunResult(result share.RunResult) RunResult { return RunResult(result) }

func toInternalCallRequest(req CallRequest) share.CallRequest { return share.CallRequest(req) }

func toPublicResult(result *share.Result) *Result {
	if result == nil {
		return nil
	}
	return &Result{NeedsElyphRules: result.NeedsElyphRules, DiscoveryText: (*DiscoveryText)(result.DiscoveryText), Content: result.Content, Segments: result.Segments, Warnings: result.Warnings, Data: result.Data, Metadata: result.Metadata, Outputs: result.Outputs}
}

func toInternalResult(result *Result) *share.Result {
	if result == nil {
		return nil
	}
	return &share.Result{NeedsElyphRules: result.NeedsElyphRules, DiscoveryText: (*share.DiscoveryText)(result.DiscoveryText), Content: result.Content, Segments: result.Segments, Warnings: result.Warnings, Data: result.Data, Metadata: result.Metadata, Outputs: result.Outputs}
}
