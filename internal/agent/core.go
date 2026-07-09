package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	agentcommands "elbot/internal/agent/commands"
	"elbot/internal/command"
	"elbot/internal/completion"
	"elbot/internal/config"
	"elbot/internal/contextmgr"
	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/logging"
	"elbot/internal/platform"
	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/turn"
)

type LogManager interface {
	Runtime() *slog.Logger
	Audit() *slog.Logger
	LogDir() string
}

// Agent is the minimal agent core that handles messages and commands.
type Agent struct {
	platform             platform.PlatformAdapter
	platformSenders      map[string]delivery.MessageSender
	modelRuntime         modelRuntimeState
	statePath            string
	stateModTime         time.Time
	store                storage.Store
	sessions             *session.Service
	requests             *request.Manager
	turns                *turn.Manager
	commands             *command.Router
	completion           *completion.Service
	titleGen             *titleGenerator
	soul                 SoulProvider
	promptBuilder        PromptBuilder
	toolRuntime          toolRuntimeState
	securityPolicy       *security.Policy
	contextLoader        contextmgr.Loader
	windowResolver       *contextmgr.WindowResolver
	compressor           contextmgr.Compressor
	contextConfig        config.ContextConfig
	hooks                hook.Manager
	outputs              delivery.Manager
	modelMetadata        config.ModelMetadataConfig
	compactModel         config.ModelSelection
	namingModel          config.ModelSelection
	usageMu              sync.Mutex
	lastUsage            map[string]*llm.Usage
	statusMu             sync.Mutex
	runtimeStatus        map[string]runtimestatus.Snapshot
	pendingCompact       map[string]bool
	lastSessionIDs       []string
	sessionListPageSize  int
	cleanupRetentionDays int
	idleExpiration       session.IdleExpirationConfig
	sandboxRoot          string
	logger               *slog.Logger
	auditLogger          *slog.Logger
	logReader            logging.Reader
	autoConfirmMu        sync.Mutex
	autoConfirmSession   map[string]bool
	autoConfirmTools     map[string]map[string]bool
	visionFallbackMu     sync.Mutex

	visionFallbackNotified map[string]bool
	responseTimeout        time.Duration
	discoveredTools        map[string]map[string]llm.ToolSchema
	actorID                string
	scopeID                string
	hookReloader           func() (hook.ReloadReport, error)
}

// New creates a new Agent.
func New(p platform.PlatformAdapter, client llm.LLM, model string, provider config.ProviderConfig, store storage.Store) *Agent {
	modeModels := map[string]config.ModelSelection{
		storage.SessionModeWork: {Provider: "default", Model: model},
		storage.SessionModeChat: {Provider: "default", Model: model},
	}
	return NewWithPrefixes(p, client, modeModels, provider, store, []string{"/"})
}

func NewWithPrefixes(p platform.PlatformAdapter, client llm.LLM, modeModels map[string]config.ModelSelection, provider config.ProviderConfig, store storage.Store, prefixes []string) *Agent {
	return NewWithOptions(p, client, "default", modeModels, map[string]config.ProviderConfig{"default": provider}, "", store, prefixes, session.Config{NamingConfig: session.NamingConfig{TriggerStep: 1}, DefaultMode: storage.SessionModeWork}, config.ModelSelection{}, nil, "", nil, "")
}

func NewWithOptions(p platform.PlatformAdapter, client llm.LLM, providerName string, modeModels map[string]config.ModelSelection, providers map[string]config.ProviderConfig, statePath string, store storage.Store, prefixes []string, sessionCfg session.Config, namingSelection config.ModelSelection, namingClient llm.LLM, namingModel string, namingNotifier session.NamingNotifier, soulPath string) *Agent {
	return NewWithRequestConfig(p, client, providerName, modeModels, providers, statePath, store, prefixes, sessionCfg, namingSelection, namingClient, namingModel, namingNotifier, soulPath, config.Default().LLMRequest)
}

func responseTimeout(cfg config.LLMRequestConfig) time.Duration {
	if cfg.ResponseTimeoutSeconds <= 0 {
		return 0
	}
	return time.Duration(cfg.ResponseTimeoutSeconds) * time.Second
}

func NewWithRequestConfig(p platform.PlatformAdapter, client llm.LLM, providerName string, modeModels map[string]config.ModelSelection, providers map[string]config.ProviderConfig, statePath string, store storage.Store, prefixes []string, sessionCfg session.Config, namingSelection config.ModelSelection, namingClient llm.LLM, namingModel string, namingNotifier session.NamingNotifier, soulPath string, llmRequestConfig config.LLMRequestConfig) *Agent {
	workModel := modeModels[storage.SessionModeWork]
	if workModel.Provider == "" || workModel.Model == "" {
		panic("mode_models.work provider/model is required")
	}
	provider, ok := providers[workModel.Provider]
	if !ok {
		panic("provider not found: " + workModel.Provider)
	}
	titleGen := &titleGenerator{primary: client, primaryModel: workModel.Model, naming: namingClient, namingModel: namingModel}
	clients := map[string]llm.LLM{workModel.Provider: client}
	promptSoul := SoulProvider(staticSoulProvider{Prompt: "You are a helpful assistant."})
	if soulPath != "" {
		promptSoul = &FileSoulProvider{Path: soulPath}
	}
	stateModTime := initialStateModTime(statePath)
	a := &Agent{
		platform:               p,
		platformSenders:        map[string]delivery.MessageSender{},
		modelRuntime:           newModelRuntimeState(client, workModel.Model, workModel.Provider, provider, llmRequestConfig, providers, modeModels, clients),
		statePath:              statePath,
		stateModTime:           stateModTime,
		store:                  store,
		sessions:               session.NewServiceWithConfig(store, sessionCfg, titleGen, namingNotifier),
		requests:               request.NewManager(0),
		turns:                  turn.NewManager(),
		commands:               command.NewRouter(prefixes),
		soul:                   promptSoul,
		securityPolicy:         security.DefaultPolicy(),
		contextLoader:          contextmgr.Loader{Store: store},
		contextConfig:          config.Default().Context,
		hooks:                  hook.NoopManager{},
		outputs:                delivery.NewManager(nil, nil),
		compactModel:           config.ModelSelection{},
		namingModel:            namingSelection,
		lastUsage:              map[string]*llm.Usage{},
		runtimeStatus:          map[string]runtimestatus.Snapshot{},
		pendingCompact:         map[string]bool{},
		autoConfirmSession:     map[string]bool{},
		autoConfirmTools:       map[string]map[string]bool{},
		visionFallbackNotified: map[string]bool{},
		responseTimeout:        responseTimeout(llmRequestConfig),

		discoveredTools: map[string]map[string]llm.ToolSchema{},

		sessionListPageSize:  config.Default().View.SessionListPageSize,
		cleanupRetentionDays: 30,
		idleExpiration:       sessionIdleExpirationConfig(config.Default().Session.IdleExpiration),
		sandboxRoot:          config.Default().Sandbox.Root,
		actorID:              "cli:local",
		scopeID:              "local",
	}
	a.rebuildSystemPrompt()
	a.attachLLMRetryNotifier(client, workModel.Provider)
	if namingClient != nil && namingClient != client {
		a.attachLLMRetryNotifier(namingClient, namingSelection.Provider)
	}
	if p != nil {
		a.platformSenders[p.Name()] = p
	}
	a.SetContextOptions(a.contextConfig, config.ModelMetadataConfig{}, providers, config.ModelSelection{})
	if err := agentcommands.RegisterDefaultModules(a.commands, agentcommands.Deps{
		Router:               a.commands,
		Sessions:             a.sessions,
		Requests:             a.requests,
		Turns:                a.turns,
		Store:                a.store,
		Scope:                a.scope,
		Models:               a,
		Compact:              a,
		ContextStatus:        a,
		Tools:                a,
		Hooks:                a,
		SetLastSessions:      a.setLastSessions,
		LastSessions:         a.lastSessions,
		SessionListPageSize:  a.sessionPageSize,
		CleanupRetentionDays: a.retentionDays,
		Audit:                a.audit,
		Logs:                 a,
		RuntimeStatus:        a.runtimeStatusForSession,
	}); err != nil {
		panic(err)
	}
	a.completion = completion.NewService(
		completion.RiskConfirmationSource{Router: a.commands, Sessions: a.sessions, Turns: a.turns, Scope: a.scope, CommandNames: riskConfirmationCommandNames()},
		completion.ForkMessageSource{Router: a.commands, Sessions: a.sessions, Store: a.store, Scope: a.scope},
		completion.ToolDirectiveSource{
			Registry:       func() *tool.Registry { return a.toolRuntime.registry },
			Actor:          a.actor,
			Policy:         func() *security.Policy { return a.securityPolicy },
			Tags:           a.completionToolTags,
			ToolNamesByTag: a.completionToolNamesByTag,
		},
		completion.RouterSource{Router: a.commands},
	)

	return a
}

type staticSoulProvider struct {
	Prompt string
}

func (p staticSoulProvider) SystemPrompt(context.Context, string) (string, error) {
	return p.Prompt, nil
}

func cloneModeModels(models map[string]config.ModelSelection) map[string]config.ModelSelection {
	out := map[string]config.ModelSelection{}
	for mode, model := range models {
		out[mode] = model
	}
	return out
}

// HandleMessage dispatches commands and chat messages.
func inboundSegments(ctx context.Context, text string) []llm.MessageSegment {
	if msg, ok := platform.MessageContextFrom(ctx); ok && len(msg.Segments) > 0 {
		return platformSegmentsToLLM(msg.Segments, text)
	}
	return llm.TextSegments(text)
}

func inboundContextSegments(ctx context.Context, text string) []llm.MessageSegment {
	if msg, ok := platform.MessageContextFrom(ctx); ok {
		if len(msg.ContextSegments) > 0 {
			return platformSegmentsToLLM(msg.ContextSegments, msg.ContextText)
		}
		if strings.TrimSpace(msg.ContextText) != "" {
			return llm.TextSegments(msg.ContextText)
		}
	}
	return inboundSegments(ctx, text)
}

func withInboundSegments(ctx context.Context, segments []llm.MessageSegment) context.Context {
	msg, ok := platform.MessageContextFrom(ctx)
	if !ok {
		return ctx
	}
	msg.Segments = llmSegmentsToPlatform(segments)
	msg.ContextText = ""
	msg.ContextSegments = nil
	return platform.WithMessageContext(ctx, msg)
}

func replaceInboundTextSegments(ctx context.Context, text string) []llm.MessageSegment {
	segments := inboundSegments(ctx, text)
	out := make([]llm.MessageSegment, 0, len(segments)+1)
	textAdded := false
	for _, segment := range segments {
		if segment.Type == llm.SegmentText {
			if !textAdded && strings.TrimSpace(text) != "" {
				out = append(out, llm.MessageSegment{Type: llm.SegmentText, Text: text})
				textAdded = true
			}
			continue
		}
		out = append(out, segment)
	}
	if !textAdded && strings.TrimSpace(text) != "" {
		out = append([]llm.MessageSegment{{Type: llm.SegmentText, Text: text}}, out...)
	}
	return out
}

func hasInboundNonTextSegment(ctx context.Context) bool {
	for _, segment := range inboundSegments(ctx, "") {
		if segment.Type != llm.SegmentText {
			return true
		}
	}
	return false
}

func llmSegmentsToPlatform(segments []llm.MessageSegment) []platform.MessageSegment {
	out := make([]platform.MessageSegment, 0, len(segments))
	for _, segment := range segments {
		switch segment.Type {
		case llm.SegmentText:
			out = append(out, platform.MessageSegment{Type: platform.SegmentText, Text: segment.Text})
		case llm.SegmentImage:
			out = append(out, platform.MessageSegment{Type: platform.SegmentImage, Text: segment.Text, URL: segment.URL, MIMEType: segment.MIMEType, Name: segment.Name})
		case llm.SegmentFile:
			out = append(out, platform.MessageSegment{Type: platform.SegmentFile, Text: segment.Text, URL: segment.URL, MIMEType: segment.MIMEType, Name: segment.Name})
		}
	}
	return out
}

func (a *Agent) CommandInfos() []command.Info {
	if a == nil || a.commands == nil {
		return nil
	}
	return a.commands.Commands()
}

func (a *Agent) HandleMessage(ctx context.Context, text string) (err error) {
	actor := a.actor(ctx)
	ctx = security.WithPolicy(security.WithActor(ctx, actor), a.securityPolicy)
	segments := inboundSegments(ctx, text)
	defer func() {
		if err != nil {
			a.notifyHookError(ctx, hook.Event{Point: hook.PointAgentInputPrepared, Actor: actorContext(actor), Message: hook.MessagePayload{Role: string(llm.RoleUser), Segments: segments}}, err)
			if shouldNotifyUserError(err) {
				a.sendChat(ctx, "请求失败："+err.Error())
			}
		}
	}()
	woken := a.messageWakeup(ctx, llm.SegmentsTextOnly(segments))
	event, err := a.runHook(ctx, hook.Event{Point: hook.PointPlatformMessageReceived, Actor: actorContext(actor), Message: hook.MessagePayload{Role: string(llm.RoleUser), Segments: segments}})
	if err != nil {
		return err
	}
	if len(event.Outputs) > 0 {
		if err := a.sendOutputs(ctx, event.Outputs); err != nil {
			return err
		}
	}
	if event.Control.Consume {
		return nil
	}
	hookChangedSegments := !reflect.DeepEqual(event.Message.Segments, segments)
	if hookChangedSegments {
		segments = event.Message.Segments
	} else {
		segments = inboundContextSegments(ctx, text)
	}
	ctx = withInboundSegments(ctx, segments)
	text = llm.SegmentsTextOnly(segments)
	if !woken {
		return nil
	}
	text = a.stripWakeupPrefix(ctx, text)
	segments = replaceInboundTextSegments(ctx, text)
	ctx = withInboundSegments(ctx, segments)
	if a.commands.IsCommand(text) {
		session, sessionErr := a.sessions.Current(ctx, a.scope(ctx))
		parsed := a.commands.Parse(text)
		if sessionErr == nil && a.turns.Snapshot(session.ID).Phase == turn.PhaseAwaitAppendConfirm {
			if turn.IsConfirm(text) || turn.IsCancel(text) {
				return a.handleAppendConfirmationInput(ctx, session, text)
			}
			if shouldBlockCommandDuringAppendConfirmation(parsed.Name) {
				a.sendChat(ctx, appendConfirmationCommandBlockedText(text))
				return nil
			}
		}
		if sessionErr == nil && a.turns.Snapshot(session.ID).Phase == turn.PhaseAwaitRiskConfirm && isRiskConfirmationCommand(text, a.commands) {
			return a.handleRiskConfirmationInput(ctx, session.ID, text)
		}
		if parsed.OK && parsed.Name != "" {
			if info, ok := a.commands.CommandInfo(parsed.Name); ok {
				if info.MinRole != security.RoleUser && actor.Role != security.RoleSuperadmin {
					a.audit("permission_denied", "actor_id", actor.ID, "command", text, "reason", "slash_command_requires_superadmin")
					a.sendChat(ctx, fmt.Sprintf("命令 %s%s 需要超级管理员权限。", parsed.Prefix, parsed.Name))
					return nil
				}
			}
		}
		result, dispatchErr := a.commands.Dispatch(ctx, text)
		if dispatchErr != nil {
			return dispatchErr
		}
		if result != nil && result.Content != "" {
			if err := a.sendNoticeOutput(ctx, delivery.Target{}, delivery.Text(result.Content)); err != nil {
				return err
			}
		}
		return nil
	}
	return a.handleInput(ctx, text)
}

func shouldNotifyUserError(err error) bool {
	var notified userNotifiedError
	return err != nil && !errors.As(err, &notified) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

type userNotifiedError struct {
	err error
}

func (e userNotifiedError) Error() string { return e.err.Error() }

func (e userNotifiedError) Unwrap() error { return e.err }

func markUserNotified(err error) error {
	if err == nil {
		return nil
	}
	return userNotifiedError{err: err}
}

func (a *Agent) scope(ctx context.Context) session.Scope {
	actor := a.actor(ctx)
	platformName := a.platform.Name()
	scopeID := a.scopeID
	if msg, ok := platform.MessageContextFrom(ctx); ok {
		if msg.Platform != "" {
			platformName = msg.Platform
		}
		if msg.ScopeID != "" {
			scopeID = msg.ScopeID
		}
	}
	return session.Scope{
		ActorID:         actor.ID,
		Platform:        platformName,
		PlatformScopeID: scopeID,
		IsCLI:           platformName == "cli" && actor.Role == security.RoleSuperadmin,
	}
}

func (a *Agent) actor(ctx context.Context) security.Actor {
	if actor, ok := security.ActorFromContext(ctx); ok && (actor.ID != "" || actor.Role != "") {
		return actor
	}
	platformName := a.platform.Name()
	platformUserID := a.actorID
	displayName := ""
	actorID := ""
	groupRole := security.GroupRoleUnknown
	if msg, ok := platform.MessageContextFrom(ctx); ok {
		if msg.Platform != "" {
			platformName = msg.Platform
		}
		if msg.PlatformUserID != "" {
			platformUserID = msg.PlatformUserID
		}
		actorID = msg.ActorID
		displayName = msg.DisplayName
		groupRole = security.ParseGroupRole(string(msg.GroupRole))
	}
	if prefix := platformName + ":"; strings.HasPrefix(platformUserID, prefix) {
		platformUserID = strings.TrimPrefix(platformUserID, prefix)
	}
	policy := a.securityPolicy
	if policy == nil {
		policy = security.DefaultPolicy()
	}
	actor := policy.Actor(actorID, platformName, platformUserID, displayName)
	actor.GroupRole = groupRole
	return actor
}

func (a *Agent) SetSessionListPageSize(size int) {
	if size <= 0 {
		size = config.Default().View.SessionListPageSize
	}
	a.sessionListPageSize = size
}

func (a *Agent) sessionPageSize() int {
	if a.sessionListPageSize <= 0 {
		return config.Default().View.SessionListPageSize
	}
	return a.sessionListPageSize
}

func (a *Agent) SetCleanupRetentionDays(days int) {
	if days <= 0 {
		days = 30
	}
	a.cleanupRetentionDays = days
}

func (a *Agent) SetSessionIdleExpiration(cfg config.SessionIdleExpirationConfig) {
	a.idleExpiration = sessionIdleExpirationConfig(cfg)
}

func sessionIdleExpirationConfig(cfg config.SessionIdleExpirationConfig) session.IdleExpirationConfig {
	return session.IdleExpirationConfig{
		GroupUserTTLMinutes:         cfg.GroupUserTTLMinutes,
		GroupSuperadminTTLMinutes:   cfg.GroupSuperadminTTLMinutes,
		PrivateUserTTLMinutes:       cfg.PrivateUserTTLMinutes,
		PrivateSuperadminTTLMinutes: cfg.PrivateSuperadminTTLMinutes,
	}
}

func (a *Agent) SetSandboxRoot(root string) {
	root = filepath.Clean(root)
	if root == "." || root == "" {
		root = config.Default().Sandbox.Root
	}
	a.sandboxRoot = root
}

func (a *Agent) retentionDays() int {
	if a.cleanupRetentionDays <= 0 {
		return 30
	}
	return a.cleanupRetentionDays
}

func (a *Agent) SetLogger(logger *slog.Logger) {
	a.logger = logger
}

func (a *Agent) SetLogManager(logs LogManager) {
	if logs == nil {
		a.logger = nil
		a.auditLogger = nil
		return
	}
	a.logger = logs.Runtime()
	a.auditLogger = logs.Audit()
	a.logReader = logging.Reader{Dir: logs.LogDir()}
}

func (a *Agent) QueryLogs(ctx context.Context, query logging.LogQuery) ([]logging.LogEntry, error) {
	return a.logReader.Query(ctx, query)
}

func (a *Agent) audit(event string, attrs ...any) {
	a.auditLog(slog.LevelInfo, event, attrs...)
}

func (a *Agent) auditDebug(event string, attrs ...any) {
	a.auditLog(slog.LevelDebug, event, attrs...)
}

func (a *Agent) auditWarn(event string, attrs ...any) {
	a.auditLog(slog.LevelWarn, event, attrs...)
}

func (a *Agent) auditError(event string, attrs ...any) {
	a.auditLog(slog.LevelError, event, attrs...)
}

func (a *Agent) auditLog(level slog.Level, event string, attrs ...any) {
	if a.auditLogger == nil {
		return
	}
	attrs = append([]any{"event", event}, attrs...)
	a.auditLogger.Log(context.Background(), level, "audit event", attrs...)
}

func (a *Agent) SetOutputManager(manager delivery.Manager) {
	a.outputs = manager
}

func (a *Agent) SetHookManager(manager hook.Manager) {
	if manager == nil {
		manager = hook.NoopManager{}
	}
	if defaultManager, ok := manager.(*hook.DefaultManager); ok {
		defaultManager.SetWakeupFunc(a.hookWakeup)
		defaultManager.SetObserver(a.observeHookRun)
	}
	a.hooks = manager
}

func (a *Agent) SetHookReloader(fn func() (hook.ReloadReport, error)) {
	a.hookReloader = fn
}

func (a *Agent) HookList() []hook.Info {
	return a.hooks.List()
}

func (a *Agent) HookReload() (hook.ReloadReport, error) {
	if a.hookReloader == nil {
		return hook.ReloadReport{}, fmt.Errorf("hook reloader is not configured")
	}
	return a.hookReloader()
}

func (a *Agent) SetSecurityPolicy(policy *security.Policy) {
	if policy == nil {
		policy = security.DefaultPolicy()
	}
	a.securityPolicy = policy
}

func (a *Agent) setLastSessions(sessions []storage.SessionSummary) {
	a.lastSessionIDs = a.lastSessionIDs[:0]
	for _, session := range sessions {
		a.lastSessionIDs = append(a.lastSessionIDs, session.ID)
	}
}

func (a *Agent) lastSessions() []string {
	return append([]string(nil), a.lastSessionIDs...)
}
