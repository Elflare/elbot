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
	"elbot/internal/output"
	"elbot/internal/platform"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

type cronModelSelectionKey struct{}

type discardSender struct{}

func (discardSender) SendChat(ctx context.Context, out output.Output) (platform.Receipt, error) {
	return platform.Receipt{}, nil
}

func (discardSender) SendNotice(ctx context.Context, target output.Target, out output.Output) (platform.Receipt, error) {
	return platform.Receipt{}, nil
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
	if injected := a.preloadToolNames(ctx, bgSession, backgroundToolListNames(req.ToolListNames)); len(injected) > 0 {
		a.audit("background_tool_preloaded", "session_id", bgSession.ID, "kind", req.Kind, "name", req.Name, "tools", injected)
	}
	if err := a.startBackgroundChat(ctx, bgSession, req.Prompt); err != nil {
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
		return a.store.Sessions().Get(ctx, req.SessionID)
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = backgroundTitle(req.Kind, req.Name)
	}
	bgSession := &storage.Session{OwnerID: scope.ActorID, Platform: scope.Platform, PlatformScopeID: scope.PlatformScopeID, Mode: a.sessions.DefaultMode(), Title: title, Status: storage.SessionStatusActive, Metadata: backgroundSessionMetadata(req)}
	if err := a.store.Sessions().Create(ctx, bgSession); err != nil {
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
	data := map[string]any{"title_renamed": true, "title_source": req.Kind, "background_kind": req.Kind, "background_name": req.Name}
	for key, value := range req.Metadata {
		if strings.TrimSpace(key) != "" {
			data[key] = value
		}
	}
	encoded, _ := json.Marshal(data)
	return string(encoded)
}

func cronSessionMetadata(jobName, sourceSessionID string, copied bool) string {
	data, _ := json.Marshal(map[string]any{"title_renamed": true, "title_source": background.KindCron, "background_kind": background.KindCron, "background_name": jobName, "cron_job_name": jobName, "cron_source_session_id": sourceSessionID, "cron_broadcast_copy": copied})
	return string(data)
}
