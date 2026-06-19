package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"elbot/internal/background"
	"elbot/internal/config"
	elcron "elbot/internal/cron"
	"elbot/internal/delivery"
	"elbot/internal/platform"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

type cronModelSelectionKey struct{}

type discardSender struct{}

func (discardSender) SendChat(ctx context.Context, out delivery.Output) (delivery.Receipt, error) {
	return delivery.Receipt{}, nil
}

func (discardSender) SendNotice(ctx context.Context, target delivery.Target, out delivery.Output) (delivery.Receipt, error) {
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
	ctx = platform.WithMessageContext(ctx, platform.MessageContext{Platform: platformName, ActorID: actor.ID, PlatformUserID: actor.PlatformUserID, DisplayName: actor.DisplayName, ScopeID: scopeID, Sender: discardSender{}})
	ctx = security.WithPolicy(security.WithActor(ctx, actor), a.securityPolicy)

	sandboxRoot := a.sandboxRoot
	if sandboxRoot == "" {
		sandboxRoot = filepath.Join("data", "sandbox")
	}
	artifactDir := a.artifactDir
	if artifactDir == "" {
		artifactDir = filepath.Join(sandboxRoot, "artifact")
	}
	sandboxSubdir := strings.TrimSpace(req.SandboxSubdir)
	if sandboxSubdir == "" {
		sandboxSubdir = strings.TrimSpace(string(req.Kind))
	}
	if sandboxSubdir == "" {
		sandboxSubdir = "background"
	}
	ctx = tool.WithSandboxContext(ctx, tool.SandboxContext{Root: sandboxRoot, Dir: filepath.Join(sandboxRoot, filepath.FromSlash(sandboxSubdir)), ArtifactDir: artifactDir, Background: true, BackgroundKind: toolBackgroundKind(req.Kind)})

	if req.ModelProvider != "" || req.Model != "" {
		ctx = context.WithValue(ctx, cronModelSelectionKey{}, config.ModelSelection{Provider: req.ModelProvider, Model: req.Model})
	}

	scope := session.Scope{ActorID: actor.ID, Platform: platformName, PlatformScopeID: scopeID, IsCLI: platformName == "cli"}
	bgSession, err := a.backgroundSession(ctx, req, scope)
	if err != nil {
		return background.RunResult{}, err
	}
	if len(req.CachedTools) > 0 {
		a.rememberCachedTools(ctx, bgSession, req.CachedTools)
		a.audit("background_external_tools_preloaded", "session_id", bgSession.ID, "kind", req.Kind, "name", req.Name, "tools", cachedToolNames(req.CachedTools))
	}
	preloaded := a.preloadBackgroundResources(ctx, bgSession, backgroundToolListNames(req.ToolListNames))
	if len(preloaded.Tools) > 0 {
		a.audit("background_tool_preloaded", "session_id", bgSession.ID, "kind", req.Kind, "name", req.Name, "tools", preloaded.Tools)
	}
	if len(preloaded.Skills) > 0 {
		a.audit("background_skill_preloaded", "session_id", bgSession.ID, "kind", req.Kind, "name", req.Name, "skills", preloaded.Skills)
	}
	prompt := backgroundPromptWithSkills(req.Prompt, preloaded.SkillPrompt)
	if err := a.startBackgroundChat(ctx, bgSession, prompt); err != nil {
		return background.RunResult{}, err
	}
	text, err := a.latestAssistantText(ctx, bgSession.ID)
	if err != nil {
		return background.RunResult{}, err
	}
	return background.RunResult{SessionID: bgSession.ID, Text: text}, nil
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
	bgSession := &storage.Session{OwnerID: scope.ActorID, Platform: scope.Platform, PlatformScopeID: scope.PlatformScopeID, Mode: storage.SessionModeWork, Title: title, Status: storage.SessionStatusActive, Metadata: backgroundSessionMetadata(req)}
	if err := a.store.Sessions().Create(ctx, bgSession); err != nil {
		return nil, err
	}
	return bgSession, nil
}

func (a *Agent) ensureBackgroundSession(ctx context.Context, bgSession *storage.Session, req background.RunRequest) (*storage.Session, error) {
	if bgSession == nil {
		return nil, storage.ErrNotFound
	}
	metadata := mergeBackgroundSessionMetadata(bgSession.Metadata, req)
	if bgSession.Mode == storage.SessionModeWork && bgSession.Metadata == metadata {
		return bgSession, nil
	}
	bgSession.Mode = storage.SessionModeWork
	bgSession.Metadata = metadata
	bgSession.UpdatedAt = storage.Now()
	if err := a.store.Sessions().Update(ctx, bgSession); err != nil {
		return nil, err
	}
	return bgSession, nil
}

func (a *Agent) latestAssistantText(ctx context.Context, sessionID string) (string, error) {
	messages, err := a.store.Messages().ListBySession(ctx, sessionID)
	if err != nil {
		return "", err
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != storage.RoleAssistant {
			continue
		}
		if rawText := assistantRawTextFromMetadata(messages[i].Metadata); rawText != "" {
			return rawText, nil
		}
		return messages[i].Content, nil
	}
	return "", fmt.Errorf("background session %s has no assistant message", sessionID)
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
	Tools       []string
	Skills      []string
	SkillPrompt string
}

func (a *Agent) preloadBackgroundResources(ctx context.Context, session *storage.Session, names []string) backgroundPreloadResult {
	result := backgroundPreloadResult{}
	if session == nil || session.Mode != storage.SessionModeWork || a.toolRuntime.registry == nil {
		return result
	}
	policy := a.securityPolicy
	if policy == nil {
		policy = security.DefaultPolicy()
	}
	actor := a.actor(ctx)
	seenInjected := map[string]bool{}
	seenSkills := map[string]bool{}
	var skillSections []string
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		candidate, ok := a.toolRuntime.registry.Get(name)
		if !ok || !tool.CanAccessTool(actor, policy, candidate.Info()) {
			a.audit("background_preload_skipped", "session_id", session.ID, "name", name, "reason", "not_found_or_not_allowed")
			continue
		}
		if detailer, ok := candidate.(tool.DetailProvider); ok {
			if candidate.Info().Hidden {
				a.audit("background_preload_skipped", "session_id", session.ID, "name", name, "reason", "hidden_skill")
				continue
			}
			detail := strings.TrimSpace(detailer.Detail())
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
				result.Tools = append(result.Tools, a.preloadBackgroundTool(ctx, session, wrapper, actor, policy, seenInjected, true)...)
			}
			continue
		}
		result.Tools = append(result.Tools, a.preloadBackgroundTool(ctx, session, name, actor, policy, seenInjected, false)...)
	}
	if len(skillSections) > 0 {
		result.SkillPrompt = strings.Join(skillSections, "\n\n---\n\n")
	}
	return result
}

func (a *Agent) preloadBackgroundTool(ctx context.Context, session *storage.Session, name string, actor security.Actor, policy *security.Policy, seen map[string]bool, allowHidden bool) []string {
	name = strings.TrimSpace(name)
	if name == "" || name == "discover_tool" {
		return nil
	}
	candidate, ok := a.toolRuntime.registry.Get(name)
	if !ok || !tool.CanAccessTool(actor, policy, candidate.Info()) || (!allowHidden && candidate.Info().Hidden) {
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
		discovery = &tool.DiscoveryResult{Tools: []tool.DiscoveredTool{{Info: tool.PublicInfo{Name: info.Name, Description: info.Description, Source: string(info.Source)}, Schema: &schema}}}
		discovered = true
	} else {
		discovery, discovered = a.discoveryForBackgroundToolNames([]string{name}, actor, policy)
	}
	if !discovered || discovery == nil || len(discovery.Tools) == 0 {
		a.audit("background_preload_skipped", "session_id", session.ID, "name", name, "reason", "no_schema")
		return nil
	}
	newTools, _ := a.rememberPreloadedDiscovery(ctx, session, discovery, seen)
	return newTools
}

func (a *Agent) discoveryForBackgroundToolNames(names []string, actor security.Actor, policy *security.Policy) (*tool.DiscoveryResult, bool) {
	details, _ := a.toolRuntime.registry.DiscoverDetails(names, func(candidate tool.Tool) bool {
		return tool.CanAccessTool(actor, policy, candidate.Info())
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

func toolBackgroundKind(kind background.Kind) tool.BackgroundKind {
	switch kind {
	case background.KindCron:
		return tool.BackgroundKindCron
	case background.KindElnis:
		return tool.BackgroundKindElnis
	default:
		return tool.BackgroundKind(strings.TrimSpace(string(kind)))
	}
}

func backgroundSessionMetadata(req background.RunRequest) string {
	data := backgroundMetadataMap(req)
	encoded, _ := json.Marshal(data)
	return string(encoded)
}

func mergeBackgroundSessionMetadata(raw string, req background.RunRequest) string {
	data := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &data)
	}
	for key, value := range backgroundMetadataMap(req) {
		data[key] = value
	}
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
