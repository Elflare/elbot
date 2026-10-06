package agent

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"

	"elbot/internal/background"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/platform"
	sandboxctx "elbot/internal/sandbox"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/toolrun"
)

type backgroundRunner struct {
	sessions    *session.Service
	sessionRows storage.SessionRepository
	identity    *identityResolver
	execution   *executionCoordinator
	preloader   *toolrun.PreloadService
	toolState   *toolrun.StateService
	sandboxRoot string
	auditLogger *slog.Logger
}

func (a *Agent) RunBackground(ctx context.Context, req background.RunRequest) (background.RunResult, error) {
	return a.background.RunBackground(ctx, req)
}

type discardSender struct{}

func (discardSender) SendChat(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, error) {
	return delivery.Receipt{}, nil
}

func (discardSender) SendNotice(context.Context, delivery.Notice) (delivery.Receipt, error) {
	return delivery.Receipt{}, nil
}

func (r *backgroundRunner) RunBackground(ctx context.Context, req background.RunRequest) (background.RunResult, error) {
	actor := req.Actor
	if actor.Role == "" {
		actor.Role = contextinfo.RoleSuperadmin
	}
	platformName := req.Platform
	if platformName == "" {
		platformName = actor.Platform
	}
	if platformName == "" {
		platformName = r.identity.platformName
	}
	scopeID := req.ScopeID
	if scopeID == "" {
		scopeID = backgroundScopeID(req.Kind, req.Name)
	}
	ctx = platform.WithMessageContext(ctx, platform.MessageContext{Conversation: contextinfo.Conversation{Source: contextinfo.Source{Platform: platformName, ScopeID: scopeID}, Identity: contextinfo.Identity{ActorID: actor.ID, PlatformUserID: actor.PlatformUserID, Nickname: actor.Nickname, GroupCard: actor.GroupCard, DisplayName: actor.DisplayName}}, Sender: discardSender{}, Segments: backgroundPromptSegments(req.PromptSegments)})
	ctx = security.WithPolicy(contextinfo.WithActor(ctx, actor), r.identity.policy)

	sandboxRoot := r.sandboxRoot
	if sandboxRoot == "" {
		sandboxRoot = filepath.Join("data", "sandbox")
	}
	sandboxSubdir := strings.TrimSpace(req.SandboxSubdir)
	if sandboxSubdir == "" {
		sandboxSubdir = strings.TrimSpace(string(req.Kind))
	}
	if sandboxSubdir == "" {
		sandboxSubdir = "background"
	}
	ctx = sandboxctx.WithSandboxContext(ctx, sandboxctx.SandboxContext{Root: sandboxRoot, Dir: filepath.Join(sandboxRoot, filepath.FromSlash(sandboxSubdir)), Background: true, BackgroundKind: toolBackgroundKind(req.Kind)})

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
	if sandbox, ok := sandboxctx.SandboxContextFromContext(ctx); ok {
		if err := session.NewWorkspaceStore(r.sessions, r.sessionRows, bgSession.ID).EnsureWorkspaceDir(ctx, sandbox.Dir); err != nil {
			return background.RunResult{}, err
		}
	}
	var preloaded backgroundPreloadResult
	if req.SessionID == "" {
		preloaded = r.preloadBackgroundResources(ctx, bgSession, req.ToolListNames, req.CachedTools, req.AllowedToolNames)
	}
	if preloaded.Err != nil {
		return background.RunResult{SessionID: bgSession.ID}, preloaded.Err
	}
	if len(preloaded.Tools) > 0 {
		writeAudit(r.auditLogger, slog.LevelInfo, "background_tool_preloaded", "session_id", bgSession.ID, "kind", req.Kind, "name", req.Name, "tools", preloaded.Tools)
	}
	if len(preloaded.Skills) > 0 {
		writeAudit(r.auditLogger, slog.LevelInfo, "background_skill_preloaded", "session_id", bgSession.ID, "kind", req.Kind, "name", req.Name, "skills", preloaded.Skills)
	}
	prompt := backgroundPromptWithSkills(req.Prompt, preloaded.SkillPrompt)
	result := r.execution.RunBackground(ctx, bgSession, prompt)
	return background.RunResult{RunID: result.RunID, SessionID: result.SessionID, MessageID: result.MessageID, Text: result.Text, TakenOver: result.TakenOver, Outcome: result.Outcome}, result.Err
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
	Err         error
	Tools       []string
	Skills      []string
	SkillPrompt string
}

func (r *backgroundRunner) preloadBackgroundResources(ctx context.Context, row *storage.Session, names []string, initial []toolrun.CachedTool, allowed []string) backgroundPreloadResult {
	if row == nil || row.Mode != storage.SessionModeBackground {
		return backgroundPreloadResult{}
	}
	preloadCtx := contextinfo.WithActor(security.WithPolicy(ctx, r.identity.policy), r.identity.Actor(ctx))
	prepared := r.preloader.PrepareBackground(preloadCtx, row.ID, names, allowed)
	prepared.Update.Tools = append(toolrun.BackgroundCachedTools(ctx, initial), prepared.Update.Tools...)
	prepared.Update.Tools = toolrun.BackgroundCachedTools(ctx, prepared.Update.Tools)
	committed, err := commitToolState(ctx, r.toolState, row, prepared.Update)
	if err != nil {
		return backgroundPreloadResult{Err: err}
	}
	return backgroundPreloadResult{Tools: committed.Injected, Skills: prepared.Skills, SkillPrompt: prepared.SkillPrompt}
}

func backgroundPromptWithSkills(prompt, skillPrompt string) string {
	skillPrompt = strings.TrimSpace(skillPrompt)
	if skillPrompt == "" {
		return prompt
	}
	return "[系统预加载 Skill]\n\n以下 Skill 说明已由系统预加载。Skill 本体不是 top-level tool schema；可调用能力只以本次请求注入的 top-level tool schema 为准。\n\n" + skillPrompt + "\n\n[后台任务]\n\n" + strings.TrimSpace(prompt)
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
