package module

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/background"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/platform"
	sandboxctx "elbot/internal/sandbox"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/toolrun"
)

type BackgroundRunner struct {
	sessions    *session.Service
	sessionRows storage.SessionRepository
	identity    BackgroundIdentity
	execution   BackgroundExecution
	preloader   *toolrun.PreloadService
	toolState   *toolrun.StateService
	sandboxRoot string
}

type discardSender struct{}

func (discardSender) SendChat(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
	return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
}

func (discardSender) NotificationsSuppressed() bool { return true }

func (discardSender) SendTo(context.Context, delivery.Target, []delivery.Output) (delivery.Receipt, globalevents.LogResult, error) {
	return delivery.Receipt{}, globalevents.LogResult{ResultStatus: globalevents.ResultSucceeded}, nil
}

func (r *BackgroundRunner) RunBackground(ctx context.Context, req background.RunRequest) (background.RunResult, error) {
	actor := req.Actor
	if actor.Role == "" {
		actor.Role = contextinfo.RoleSuperadmin
	}
	platformName := req.Platform
	if platformName == "" {
		platformName = actor.Platform
	}
	if platformName == "" {
		platformName = r.identity.PlatformName()
	}
	scopeID := req.ScopeID
	if scopeID == "" {
		scopeID = backgroundScopeID(req.Kind, req.Name)
	}
	ctx = platform.WithMessageContext(ctx, platform.MessageContext{
		Conversation: contextinfo.Conversation{
			Source:   contextinfo.Source{Platform: platformName, ScopeID: scopeID},
			Identity: contextinfo.Identity{ActorID: actor.ID, PlatformUserID: actor.PlatformUserID, Nickname: actor.Nickname, GroupCard: actor.GroupCard, DisplayName: actor.DisplayName},
		},
		Sender:   discardSender{},
		Segments: backgroundPromptSegments(req.PromptSegments),
	})
	ctx = security.WithPolicy(contextinfo.WithActor(ctx, actor), r.identity.Policy())

	sandboxRoot := r.sandboxRoot
	sandboxSubdir := strings.TrimSpace(req.SandboxSubdir)
	if sandboxSubdir == "" {
		sandboxSubdir = strings.TrimSpace(string(req.Kind))
	}
	if sandboxSubdir == "" {
		sandboxSubdir = "background"
	}
	ctx = sandboxctx.WithSandboxContext(ctx, sandboxctx.SandboxContext{
		Root:           sandboxRoot,
		Dir:            filepath.Join(sandboxRoot, filepath.FromSlash(sandboxSubdir)),
		Background:     true,
		BackgroundKind: toolBackgroundKind(req.Kind),
	})

	if req.ModelProvider != "" || req.Model != "" {
		ctx = modelmgr.WithSelectionOverride(ctx, config.ModelSelection{Provider: req.ModelProvider, Model: req.Model})
	}

	scope := session.Scope{ActorID: actor.ID, Platform: platformName, PlatformScopeID: scopeID, IsCLI: platformName == "cli"}
	bgSession, err := r.sessions.PrepareBackground(ctx, scope, session.BackgroundRequest{SessionID: req.SessionID, Kind: string(req.Kind), Name: req.Name, Title: req.Title, Metadata: req.Metadata})
	if err != nil {
		if errors.Is(err, session.ErrForegroundSession) {
			return background.RunResult{SessionID: req.SessionID, TakenOver: true, Outcome: "taken_over"}, nil
		}
		return background.RunResult{}, err
	}
	if err := session.NewWorkspaceStore(r.sessions, r.sessionRows, bgSession.ID).EnsureWorkspaceDir(ctx, filepath.Join(sandboxRoot, filepath.FromSlash(sandboxSubdir))); err != nil {
		return background.RunResult{}, err
	}
	var preloaded backgroundPreloadResult
	if req.SessionID == "" {
		ctx, err = r.execution.ModelContext(ctx, bgSession)
		if err != nil {
			return background.RunResult{SessionID: bgSession.ID}, err
		}
		preloaded = r.preloadBackgroundResources(ctx, bgSession, req.ToolListNames, req.CachedTools, req.AllowedToolNames)
	}
	if preloaded.Err != nil {
		return background.RunResult{SessionID: bgSession.ID}, preloaded.Err
	}
	record := globalevents.LogRecord{
		Category:     globalevents.LogAudit,
		Level:        slog.LevelInfo,
		Module:       "agent",
		Type:         globalevents.ContentJSON,
		ContentLimit: 4096,
		Identity:     globalevents.LogIdentity{SessionID: &bgSession.ID},
		ResultStatus: globalevents.ResultSucceeded,
	}
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	if facts.RunID != "" {
		record.Identity.RunID = &facts.RunID
	}
	if facts.Attempt != "" {
		record.Identity.Attempt = &facts.Attempt
	}
	if facts.RequestID != "" {
		record.Identity.RequestID = &facts.RequestID
	}
	if facts.RootRequestID != "" {
		record.Identity.RootRequestID = &facts.RootRequestID
	}
	if len(preloaded.Tools) > 0 {
		content, _ := json.Marshal(map[string]any{"kind": req.Kind, "name": req.Name, "tools": preloaded.Tools})
		record.EventName, record.Content = "background_tool_preloaded", string(content)
		background.LogBackground(ctx, record)
	}
	if len(preloaded.Skills) > 0 {
		content, _ := json.Marshal(map[string]any{"kind": req.Kind, "name": req.Name, "skills": preloaded.Skills})
		record.EventName, record.Content = "background_skill_preloaded", string(content)
		background.LogBackground(ctx, record)
	}
	prompt := backgroundPromptWithSkills(req.Prompt, preloaded.SkillPrompt)
	input := agentshare.InboundTurnInput(ctx, prompt)
	input.NeedsElyphRules = req.NeedsElyphRules || preloaded.NeedsElyphRules
	ctx = agentshare.WithInboundTurnInput(ctx, input)
	result := r.execution.RunBackground(ctx, bgSession, prompt)
	return background.RunResult{
		RunID:     result.RunID,
		SessionID: result.SessionID,
		MessageID: result.MessageID,
		Text:      result.Text,
		TakenOver: result.TakenOver,
		Outcome:   result.Outcome,
	}, result.Err
}

func backgroundPromptSegments(segments []llm.MessageSegment) []platform.MessageSegment {
	out := make([]platform.MessageSegment, 0, len(segments))
	for _, segment := range segments {
		switch segment.Type {
		case llm.SegmentText:
			out = append(out, platform.MessageSegment{Type: platform.SegmentText, Text: segment.Text, MIMEType: segment.MIMEType, Name: segment.Name})
		case llm.SegmentImage:
			out = append(out, platform.MessageSegment{Type: platform.SegmentImage, Text: segment.Text, URL: segment.URL, MediaID: segment.MediaID, MIMEType: segment.MIMEType, Name: segment.Name})
		case llm.SegmentFile:
			out = append(out, platform.MessageSegment{Type: platform.SegmentFile, Text: segment.Text, URL: segment.URL, MediaID: segment.MediaID, MIMEType: segment.MIMEType, Name: segment.Name})
		}
	}
	return out
}

func backgroundScopeID(kind background.Kind, name string) string {
	kindText := strings.TrimSpace(string(kind))
	name = strings.TrimSpace(name)
	if kindText == "" {
		kindText = "background"
	}
	if name == "" {
		return kindText + ":default"
	}
	return kindText + ":" + name
}

type backgroundPreloadResult struct {
	NeedsElyphRules bool
	Err             error
	Tools           []string
	Skills          []string
	SkillPrompt     string
}

func (r *BackgroundRunner) preloadBackgroundResources(ctx context.Context, row *storage.Session, names []string, initial []toolrun.CachedTool, allowed []string) backgroundPreloadResult {
	preloadCtx := contextinfo.WithActor(security.WithPolicy(ctx, r.identity.Policy()), r.identity.Actor(ctx))
	prepared, err := r.preloader.PrepareBackground(preloadCtx, row.ID, names, allowed)
	if err != nil {
		return backgroundPreloadResult{Err: err}
	}
	prepared.Update.Tools = append(toolrun.BackgroundCachedTools(ctx, initial), prepared.Update.Tools...)
	prepared.Update.Tools = toolrun.BackgroundCachedTools(ctx, prepared.Update.Tools)
	committed, _, err := agentshare.CommitToolState(ctx, r.toolState, row, prepared.Update)
	if err != nil {
		return backgroundPreloadResult{Err: err}
	}
	return backgroundPreloadResult{Tools: committed.Injected, Skills: prepared.Skills, SkillPrompt: prepared.SkillPrompt, NeedsElyphRules: prepared.NeedsElyphRules}
}

func backgroundPromptWithSkills(prompt, skillPrompt string) string {
	skillPrompt = strings.TrimSpace(skillPrompt)
	if skillPrompt == "" {
		return prompt
	}
	return "[系统预加载资源]\n\n以下工具使用说明与 Skill 文档已由系统预加载。可调用能力以本次请求注入的 top-level tool schema 为准。\n\n" + skillPrompt + "\n\n[后台任务]\n\n" + strings.TrimSpace(prompt)
}

func toolBackgroundKind(kind background.Kind) sandboxctx.BackgroundKind {
	switch kind {
	case background.KindCron:
		return sandboxctx.BackgroundKindCron
	case background.KindElnis:
		return sandboxctx.BackgroundKindElnis
	default:
		return sandboxctx.BackgroundKind(strings.TrimSpace(string(kind)))
	}
}

// NewBackgroundRunner initializes BackgroundRunner without installing cross-service callbacks.
func NewBackgroundRunner(deps BackgroundRunnerDeps) *BackgroundRunner {
	return &BackgroundRunner{
		sessions: deps.Sessions, sessionRows: deps.SessionRows, identity: deps.Identity,
		execution: deps.Execution, preloader: deps.Preloader, toolState: deps.ToolState,
		sandboxRoot: filepath.Clean(deps.SandboxRoot),
	}
}
