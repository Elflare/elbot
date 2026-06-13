package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

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
		scopeID = "cron:" + req.JobName
	}
	// cron 后台运行没有真实前台会话，使用 discardSender 吃掉普通聊天输出；
	// 最终报告由 cron service 解析 LLM 返回 JSON 后再投递到目标平台。
	ctx = platform.WithMessageContext(ctx, platform.MessageContext{Platform: platformName, ActorID: actor.ID, PlatformUserID: actor.PlatformUserID, DisplayName: actor.DisplayName, ScopeID: scopeID, Sender: discardSender{}, BufferAssistantOutput: true})
	ctx = security.WithPolicy(security.WithActor(ctx, actor), a.securityPolicy)
	// 后台 cron shell 使用统一 sandbox 下的 cron 子目录，上下文只在本次执行传播，
	// 不写入 Session，避免影响普通对话工具调用。
	sandboxRoot := a.sandboxRoot
	if sandboxRoot == "" {
		sandboxRoot = filepath.Join("data", "sandbox")
	}
	artifactDir := a.artifactDir
	if artifactDir == "" {
		artifactDir = filepath.Join(sandboxRoot, "artifact")
	}
	ctx = tool.WithSandboxContext(ctx, tool.SandboxContext{Root: sandboxRoot, Dir: filepath.Join(sandboxRoot, "cron"), ArtifactDir: artifactDir, CronBackground: true})

	if req.ModelProvider != "" || req.Model != "" {
		ctx = context.WithValue(ctx, cronModelSelectionKey{}, config.ModelSelection{Provider: req.ModelProvider, Model: req.Model})
	}

	cronScope := session.Scope{ActorID: actor.ID, Platform: platformName, PlatformScopeID: scopeID, IsCLI: platformName == "cli"}
	cronSession, err := a.cronSession(ctx, req, cronScope)
	if err != nil {
		return elcron.RunCronMessageResult{}, err
	}
	if injected := a.preloadToolNames(ctx, cronSession, req.ToolListNames); len(injected) > 0 {
		a.audit("cron_tool_preloaded", "session_id", cronSession.ID, "job", req.JobName, "tools", injected)
	}
	if err := a.startChat(ctx, cronSession, req.Prompt); err != nil {

		return elcron.RunCronMessageResult{}, err
	}
	text, err := a.latestAssistantText(ctx, cronSession.ID)
	if err != nil {
		return elcron.RunCronMessageResult{}, err
	}
	return elcron.RunCronMessageResult{SessionID: cronSession.ID, Text: text}, nil
}

func (a *Agent) cronSession(ctx context.Context, req elcron.RunCronMessageRequest, scope session.Scope) (*storage.Session, error) {
	if req.SessionID != "" {
		return a.store.Sessions().Get(ctx, req.SessionID)
	}
	title := req.Title
	if title == "" {
		title = "Cron: " + req.JobName
	}
	cronSession := &storage.Session{OwnerID: scope.ActorID, Platform: scope.Platform, PlatformScopeID: scope.PlatformScopeID, Mode: a.sessions.DefaultMode(), Title: title, Status: storage.SessionStatusActive, Metadata: cronSessionMetadata(req.JobName, "", false)}
	if err := a.store.Sessions().Create(ctx, cronSession); err != nil {
		return nil, err
	}
	return cronSession, nil
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
	return "", fmt.Errorf("cron session %s has no assistant message", sessionID)
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

func cronSessionMetadata(jobName, sourceSessionID string, copied bool) string {
	// cron session 使用固定任务语义，不希望被普通对话自动命名覆盖，
	// 因此标记 title_renamed=true，让命名流程认为标题已经稳定。
	data, _ := json.Marshal(map[string]any{"title_renamed": true, "title_source": "cron", "cron_job_name": jobName, "cron_source_session_id": sourceSessionID, "cron_broadcast_copy": copied})
	return string(data)
}
