package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"elbot/internal/background"
	"elbot/internal/chatinfo"
	"elbot/internal/config"
	elcron "elbot/internal/cron"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/platform"
	sandboxctx "elbot/internal/sandbox"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
	"elbot/internal/turn"
)

type cronModelSelectionKey struct{}

type discardSender struct{}

func (discardSender) SendChat(ctx context.Context, outputs []delivery.Output) (delivery.Receipt, error) {
	return delivery.Receipt{}, nil
}

func (discardSender) SendNotice(context.Context, delivery.Notice) (delivery.Receipt, error) {
	return delivery.Receipt{}, nil
}

func (a *Agent) RunCronMessage(ctx context.Context, req elcron.RunCronMessageRequest) (elcron.RunCronMessageResult, error) {
	result, err := a.RunBackground(ctx, background.RunRequest{
		Kind:          background.KindCron,
		Name:          req.JobName,
		Title:         req.Title,
		Platform:      req.Platform,
		Actor:         req.Actor,
		ScopeID:       req.ScopeID,
		SessionID:     req.SessionID,
		ModelProvider: req.ModelProvider,
		Model:         req.Model,
		Prompt:        req.Prompt,
		ToolListNames: req.ToolListNames,
		SandboxSubdir: string(background.KindCron),
		Metadata:      map[string]string{"cron_job_name": req.JobName},
	})
	return elcron.RunCronMessageResult{SessionID: result.SessionID, Text: result.Text}, err
}

func (a *Agent) RunBackground(ctx context.Context, req background.RunRequest) (background.RunResult, error) {
	actor := req.Actor
	if actor.Role == "" {
		actor.Role = security.RoleSuperadmin
	}
	platformName := req.Platform
	if platformName == "" {
		platformName = actor.Platform
	}
	if platformName == "" && a.platform != nil {
		platformName = a.platform.Name()
	}
	scopeID := req.ScopeID
	if scopeID == "" {
		scopeID = backgroundScopeID(req.Kind, req.Name)
	}
	ctx = platform.WithMessageContext(ctx, platform.MessageContext{Info: chatinfo.Info{Source: chatinfo.Source{Platform: platformName, ScopeID: scopeID}, Identity: chatinfo.Identity{ActorID: actor.ID, PlatformUserID: actor.PlatformUserID, Nickname: actor.Nickname, GroupCard: actor.GroupCard, DisplayName: actor.DisplayName}}, Sender: discardSender{}, Segments: backgroundPromptSegments(req.PromptSegments)})
	ctx = security.WithPolicy(security.WithActor(ctx, actor), a.securityPolicy)

	sandboxRoot := a.sandboxRoot
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
		ctx = context.WithValue(ctx, cronModelSelectionKey{}, config.ModelSelection{Provider: req.ModelProvider, Model: req.Model})
	}

	scope := session.Scope{ActorID: actor.ID, Platform: platformName, PlatformScopeID: scopeID, IsCLI: platformName == "cli"}
	bgSession, err := a.backgroundSession(ctx, req, scope)
	if err != nil {
		if errors.Is(err, session.ErrForegroundSession) {
			return background.RunResult{SessionID: req.SessionID, TakenOver: true, Outcome: "taken_over"}, nil
		}
		return background.RunResult{}, err
	}
	if sandbox, ok := sandboxctx.SandboxContextFromContext(ctx); ok {
		if err := a.workspaceStore(bgSession).EnsureWorkspaceDir(ctx, sandbox.Dir); err != nil {
			return background.RunResult{}, err
		}
	}
	preloaded := a.preloadBackgroundResources(ctx, bgSession, backgroundToolListNames(req.ToolListNames), req.CachedTools)
	if preloaded.Err != nil {
		return background.RunResult{SessionID: bgSession.ID}, preloaded.Err
	}
	if len(req.CachedTools) > 0 {
		names := make([]string, 0, len(req.CachedTools))
		for _, item := range req.CachedTools {
			if item.Name != "" {
				names = append(names, item.Name)
			}
		}
		a.audit("background_external_tools_preloaded", "session_id", bgSession.ID, "kind", req.Kind, "name", req.Name, "tools", names)
	}
	if len(preloaded.Tools) > 0 {
		a.audit("background_tool_preloaded", "session_id", bgSession.ID, "kind", req.Kind, "name", req.Name, "tools", preloaded.Tools)
	}
	if len(preloaded.Skills) > 0 {
		a.audit("background_skill_preloaded", "session_id", bgSession.ID, "kind", req.Kind, "name", req.Name, "skills", preloaded.Skills)
	}
	prompt := backgroundPromptWithSkills(req.Prompt, preloaded.SkillPrompt)
	execution := turn.NewExecution(storage.NewID())
	execution.SetResult(bgSession.ID, "", "")
	ctx = turn.WithExecution(ctx, execution)
	err = a.startBackgroundChat(ctx, bgSession, prompt)
	if err != nil {
		execution.Finish(err)
	}
	result := execution.Wait(ctx)
	if latest, loadErr := a.store.Sessions().Get(context.WithoutCancel(ctx), bgSession.ID); loadErr == nil && session.WasPromoted(latest) {
		result.TakenOver = true
	}
	if result.Err != nil && ctx.Err() != nil {
		_, release, lockErr := a.sessions.EnterSessions(context.WithoutCancel(ctx), result.SessionID)
		if lockErr == nil {
			if a.turns.Execution(result.SessionID) == execution {
				a.requests.CancelSession(result.SessionID)
				a.turns.StopSession(result.SessionID)
			}
			release()
		}
	}
	return background.RunResult{RunID: result.RunID, SessionID: result.SessionID, MessageID: result.MessageID, Text: result.Text, TakenOver: result.TakenOver, Outcome: result.Outcome}, result.Err
}

func (a *Agent) backgroundSession(ctx context.Context, req background.RunRequest, scope session.Scope) (*storage.Session, error) {
	if req.SessionID != "" {
		bgSession, err := a.store.Sessions().Get(ctx, req.SessionID)
		if err != nil {
			return nil, err
		}
		return a.ensureBackgroundSession(ctx, bgSession, req)
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = backgroundTitle(req.Kind, req.Name)
	}
	bgSession := &storage.Session{OwnerID: scope.ActorID, Platform: scope.Platform, PlatformScopeID: scope.PlatformScopeID, Mode: normalizeBackgroundSessionMode(req.SessionMode), Title: title, Status: storage.SessionStatusActive, Metadata: backgroundSessionMetadata(req)}
	if err := a.store.Sessions().Create(ctx, bgSession); err != nil {
		return nil, err
	}
	return bgSession, nil
}

func (a *Agent) ensureBackgroundSession(ctx context.Context, bgSession *storage.Session, req background.RunRequest) (*storage.Session, error) {
	if bgSession == nil {
		return nil, storage.ErrNotFound
	}
	return a.store.Sessions().Mutate(ctx, bgSession.ID, func(row *storage.Session) error {
		if session.WasPromoted(row) {
			return session.ErrForegroundSession
		}
		fields, err := storage.DecodeSessionMetadata(row.Metadata)
		if err != nil {
			return err
		}
		for key, value := range backgroundMetadataMap(req) {
			if err := fields.Set(key, value); err != nil {
				return err
			}
		}
		metadata, err := fields.Encode()
		if err != nil {
			return err
		}
		row.Mode, row.Metadata, row.UpdatedAt = normalizeBackgroundSessionMode(req.SessionMode), metadata, storage.Now()
		return nil
	})
}

func (a *Agent) latestAssistantMessage(ctx context.Context, sessionID string) (storage.Message, error) {
	messages, err := a.store.Messages().ListBySession(ctx, sessionID)
	if err != nil {
		return storage.Message{}, err
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == storage.RoleAssistant {
			return messages[i], nil
		}
	}
	return storage.Message{}, fmt.Errorf("background session %s has no assistant message", sessionID)
}

func assistantRawTextFromMetadata(raw string) string {
	if raw == "" {
		return ""
	}
	var metadata assistantMetadata
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		return ""
	}
	return metadata.RawText
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

func normalizeBackgroundSessionMode(mode string) string {
	switch strings.TrimSpace(mode) {
	case storage.SessionModeChat:
		return storage.SessionModeChat
	default:
		return storage.SessionModeWork
	}
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

func backgroundTitle(kind background.Kind, name string) string {
	kindText := strings.TrimSpace(string(kind))
	name = strings.TrimSpace(name)
	if kindText == "" {
		kindText = "Background"
	}
	if name == "" {
		return strings.ToUpper(kindText[:1]) + kindText[1:]
	}
	return strings.ToUpper(kindText[:1]) + kindText[1:] + ": " + name
}

type backgroundPreloadResult struct {
	Err         error
	Tools       []string
	Skills      []string
	SkillPrompt string
}

func (a *Agent) preloadBackgroundResources(ctx context.Context, session *storage.Session, names []string, initial []toolrun.CachedTool) backgroundPreloadResult {
	result := backgroundPreloadResult{}
	if session == nil || session.Mode != storage.SessionModeWork {
		return result
	}
	if a.toolRuntime.registry == nil {
		committed, err := a.commitToolState(ctx, session, toolrun.StateUpdate{Tools: initial})
		result.Tools, result.Err = committed.Injected, err
		return result
	}
	policy := a.securityPolicy
	if policy == nil {
		policy = security.DefaultPolicy()
	}
	actor := a.actor(ctx)
	update := toolrun.StateUpdate{Tools: initial}
	seenSkills := map[string]bool{}
	var skillSections []string
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		candidate, ok := a.toolRuntime.registry.Get(name)
		if !ok || !tool.InfoAvailableInContext(ctx, candidate.Info()) || !tool.CanAccessTool(actor, policy, candidate.Info()) {
			a.audit("background_preload_skipped", "session_id", session.ID, "name", name, "reason", "not_found_or_not_allowed")
			continue
		}
		if detailer, ok := candidate.(tool.DetailProvider); ok {
			if candidate.Info().Hidden {
				a.audit("background_preload_skipped", "session_id", session.ID, "name", name, "reason", "hidden_skill")
				continue
			}
			block, err := skillDetailBlock(security.WithActor(ctx, actor), candidate, detailer)
			if err != nil {
				a.audit("background_preload_skipped", "session_id", session.ID, "name", name, "reason", "skill_detail_failed", "error", err)
				continue
			}
			detail := strings.TrimSpace(tool.RenderDetailBlocks([]tool.DetailBlock{block}))
			if detail == "" {
				a.audit("background_preload_skipped", "session_id", session.ID, "name", name, "reason", "empty_skill_detail")
				continue
			}
			if !seenSkills[name] {
				seenSkills[name] = true
				result.Skills = append(result.Skills, name)
				skillSections = append(skillSections, "## Skill: "+name+"\n\n"+detail)
			}
			for _, wrapper := range detailer.ActivateTools() {
				update.Tools = append(update.Tools, a.preloadBackgroundTool(ctx, session, wrapper, actor, policy, true)...)
			}
			continue
		}
		update.Tools = append(update.Tools, a.preloadBackgroundTool(ctx, session, name, actor, policy, false)...)
	}
	committed, err := a.commitToolState(ctx, session, update)
	if err != nil {
		return backgroundPreloadResult{Err: err}
	}
	result.Tools = committed.Injected
	if len(skillSections) > 0 {
		result.SkillPrompt = strings.Join(skillSections, "\n\n---\n\n")
	}
	return result
}

func (a *Agent) preloadBackgroundTool(ctx context.Context, session *storage.Session, name string, actor security.Actor, policy *security.Policy, allowHidden bool) []toolrun.CachedTool {
	name = strings.TrimSpace(name)
	if name == "" || name == "discover_tool" {
		return nil
	}
	candidate, ok := a.toolRuntime.registry.Get(name)
	if !ok || !tool.InfoAvailableInContext(ctx, candidate.Info()) || !tool.CanAccessTool(actor, policy, candidate.Info()) || (!allowHidden && candidate.Info().Hidden) {
		a.audit("background_preload_skipped", "session_id", session.ID, "name", name, "reason", "not_found_or_not_allowed")
		return nil
	}
	if _, isSkill := candidate.(tool.DetailProvider); isSkill {
		a.audit("background_preload_skipped", "session_id", session.ID, "name", name, "reason", "skill_has_no_schema")
		return nil
	}
	var discovery *tool.DiscoveryResult
	discovered := false
	if allowHidden && candidate.Info().Hidden {
		schema := candidate.Schema()
		info := candidate.Info()
		discovery = &tool.DiscoveryResult{Tools: []tool.DiscoveredTool{{Info: tool.PublicInfo{Name: info.Name, Description: info.Description, Source: string(info.Source), ForegroundOnly: info.ForegroundOnly}, Schema: &schema}}}
		discovered = true
	} else {
		discovery, discovered = a.discoveryForBackgroundToolNames(ctx, []string{name}, actor, policy)
	}
	if !discovered || discovery == nil || len(discovery.Tools) == 0 {
		a.audit("background_preload_skipped", "session_id", session.ID, "name", name, "reason", "no_schema")
		return nil
	}
	return toolrun.NativeCachedToolsFromDiscovery(discovery)
}

func (a *Agent) discoveryForBackgroundToolNames(ctx context.Context, names []string, actor security.Actor, policy *security.Policy) (*tool.DiscoveryResult, bool) {
	details, _ := a.toolRuntime.registry.DiscoverDetails(security.WithActor(ctx, actor), names, func(candidate tool.Tool) bool {
		info := candidate.Info()
		return tool.InfoAvailableInContext(ctx, info) && tool.CanAccessTool(actor, policy, info)
	})
	if len(details) == 0 {
		return nil, false
	}
	out := &tool.DiscoveryResult{}
	for _, discovered := range details {
		if discovered.Schema == nil || discovered.Info.Name == "" || discovered.Detail != "" {
			continue
		}
		out.Tools = append(out.Tools, discovered)
	}
	return out, len(out.Tools) > 0
}

func backgroundPromptWithSkills(prompt, skillPrompt string) string {
	skillPrompt = strings.TrimSpace(skillPrompt)
	if skillPrompt == "" {
		return prompt
	}
	return "[系统预加载 Skill]\n\n以下 Skill 说明已由系统预加载。Skill 本体不是 top-level tool schema；可调用能力只以本次请求注入的 top-level tool schema 为准。\n\n" + skillPrompt + "\n\n[后台任务]\n\n" + strings.TrimSpace(prompt)
}

func backgroundToolListNames(names []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || name == "discover_tool" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
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

func backgroundSessionMetadata(req background.RunRequest) string {
	data := backgroundMetadataMap(req)
	encoded, _ := json.Marshal(data)
	return string(encoded)
}

func backgroundMetadataMap(req background.RunRequest) map[string]any {
	data := map[string]any{"title_renamed": true, "title_source": req.Kind, "background_kind": req.Kind, "background_name": req.Name}
	for key, value := range req.Metadata {
		if strings.TrimSpace(key) != "" {
			data[key] = value
		}
	}
	if len(req.CachedTools) > 0 {
		data["tool_cache"] = req.CachedTools
	}
	return data
}

func cronSessionMetadata(jobName, sourceSessionID string, copied bool) string {
	data, _ := json.Marshal(map[string]any{"title_renamed": true, "title_source": background.KindCron, "background_kind": background.KindCron, "background_name": jobName, "cron_job_name": jobName, "cron_source_session_id": sourceSessionID, "cron_broadcast_copy": copied})
	return string(data)
}
