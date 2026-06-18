package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
	"elbot/internal/turn"
)

type agentToolRunDeps struct {
	agent  *Agent
	output turnOutput
}

func (d agentToolRunDeps) PrepareToolCall(ctx context.Context, session *storage.Session, call llm.ToolCallRequest) (llm.ToolCallRequest, error) {
	event, err := d.agent.runHook(ctx, hook.Event{
		Point:   hook.PointToolCallPrepared,
		Session: d.agent.hookSession(session),
		Tool:    hook.ToolPayload{ID: call.ID, Name: call.Name, Arguments: call.Arguments},
	})
	if err != nil {
		return call, err
	}
	call.ID = event.Tool.ID
	call.Name = event.Tool.Name
	call.Arguments = event.Tool.Arguments
	return call, nil
}

func (d agentToolRunDeps) CompleteToolCall(ctx context.Context, session *storage.Session, call llm.ToolCallRequest, risk string, result string, callErr error) (string, error) {
	event, err := d.agent.runHook(ctx, hook.Event{
		Point:   hook.PointToolCallCompleted,
		Session: d.agent.hookSession(session),
		Tool: hook.ToolPayload{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: call.Arguments,
			Risk:      risk,
			Result:    result,
			Error:     callErr,
		},
	})
	if err != nil {
		return "", err
	}
	return event.Tool.Result, nil
}

func (d agentToolRunDeps) StartToolRequest(ctx context.Context, sessionID, toolName string) (context.Context, time.Time, func(), error) {
	toolReq, toolCtx, done, err := d.agent.requests.Start(ctx, request.StartRequest{SessionID: sessionID, Kind: request.KindTool, Label: toolName})
	if err != nil {
		return ctx, time.Time{}, func() {}, err
	}
	d.output.PublishRuntimeStatus(ctx, runtimestatus.Snapshot{SessionID: sessionID, Phase: runtimestatus.PhaseTool, RequestID: toolReq.ID, Kind: request.KindTool, Label: toolName, ToolName: toolName, StageStartedAt: toolReq.StartedAt})
	return toolCtx, toolReq.StartedAt, done, nil
}

func (d agentToolRunDeps) ShouldSendPreview(ctx context.Context, session *storage.Session, call llm.ToolCallRequest, assistantText string) bool {
	return d.agent.isCLIContext(ctx) || strings.TrimSpace(assistantText) == ""
}

func (d agentToolRunDeps) ConfirmToolCall(ctx context.Context, sessionID string, call llm.ToolCallRequest, assessment tool.RiskAssessment) (toolrun.ConfirmResult, error) {
	if d.agent.isSessionAutoConfirmed(sessionID) || d.agent.isToolAutoConfirmed(sessionID, call.Name) {
		return toolrun.ConfirmResult{Allowed: true}, nil
	}
	fullArgs := compactArguments(call.Arguments)
	previewArgs := previewArguments(fullArgs)
	d.agent.logRiskConfirmationWait(sessionID, call, assessment.Level, assessment.Reasons)
	d.agent.sendChat(ctx, fmt.Sprintf("高风险工具调用等待确认\n工具：%s\n风险：%s\n参数：%s%s\n%s。", call.Name, assessment.Level, previewArgs, riskReasonsText(assessment.Reasons), riskConfirmationPromptText()))
	resp, ok := d.agent.turns.AwaitRiskConfirmation(sessionID, turn.RiskConfirmation{ID: call.ID, ToolName: call.Name, Arguments: fullArgs, Risk: string(assessment.Level), Summary: fmt.Sprintf("%s %s", call.Name, previewArgs)})
	if !ok || resp.Stopped {
		d.agent.logRiskConfirmationResult(sessionID, call, assessment.Level, "stop", resp.Extra, "")
		return toolrun.ConfirmResult{Allowed: false, Extra: resp.Extra, Message: llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID, Segments: llm.TextSegments(fmt.Sprintf("tool call %s stopped by user", call.Name))}, Stopped: true}, nil
	}
	if resp.ConfirmTool {
		d.agent.setToolAutoConfirmed(sessionID, call.Name)
		d.agent.sendChat(ctx, fmt.Sprintf("已为当前 Session 自动确认后续 %s 工具调用。", call.Name))
	}
	if resp.ConfirmAll {
		d.agent.setSessionAutoConfirmed(sessionID)
		d.agent.sendChat(ctx, "已为当前 Session 自动确认后续高风险工具调用。")
	}
	if resp.Rejected {
		reason := strings.TrimSpace(resp.Reason)
		if reason == "" {
			reason = "user rejected"
		}
		d.agent.logRiskConfirmationResult(sessionID, call, assessment.Level, "reject", resp.Extra, reason)
		return toolrun.ConfirmResult{Allowed: false, Extra: resp.Extra, Message: llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID, Segments: llm.TextSegments(fmt.Sprintf("tool call %s rejected by user: %s", call.Name, reason))}}, nil
	}
	action := "confirm"
	if resp.ConfirmTool {
		action = "confirmtool"
	}
	if resp.ConfirmAll {
		action = "confirmall"
	}
	d.agent.logRiskConfirmationResult(sessionID, call, assessment.Level, action, resp.Extra, "")
	return toolrun.ConfirmResult{Allowed: true, Extra: resp.Extra}, nil
}

func (d agentToolRunDeps) ConfirmBackgroundTool(ctx context.Context, sessionID string, call llm.ToolCallRequest, resolved toolrun.ResolvedTool, assessment tool.RiskAssessment) (toolrun.ConfirmResult, bool) {
	message := llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID}
	allowed, handled := d.agent.confirmBackgroundSandboxShell(ctx, sessionID, call, assessment.Level, &message)
	if !handled {
		return toolrun.ConfirmResult{}, false
	}
	return toolrun.ConfirmResult{Allowed: allowed, Message: message}, true
}

func (d agentToolRunDeps) SendPreview(ctx context.Context, text string) {
	d.output.SendPreview(ctx, text)
}

func (d agentToolRunDeps) SendOutputs(ctx context.Context, outputs []delivery.Output) error {
	return d.output.SendOutputs(ctx, outputs)
}

func (d agentToolRunDeps) RecordToolCall(ctx context.Context, sessionID string, call llm.ToolCallRequest, risk string, startedAt time.Time, result string, callErr error) {
	d.agent.recordToolCall(ctx, sessionID, call, risk, startedAt, result, callErr)
}

func (d agentToolRunDeps) AuditToolDenied(ctx context.Context, sessionID string, call llm.ToolCallRequest, risk tool.RiskLevel, reason string) {
	d.agent.audit("permission_denied", "actor_id", d.agent.actor(ctx).ID, "session_id", sessionID, "tool", call.Name, "risk", risk, "reason", reason)
}

func (d agentToolRunDeps) RememberDiscoveryResult(ctx context.Context, session *storage.Session, result *tool.Result) {
	d.agent.rememberDiscoveryResult(ctx, session, result)
}

func (d agentToolRunDeps) AddToolUse(sessionID, toolName string) {
	d.agent.turns.AddToolUse(sessionID, toolName)
}

func (d agentToolRunDeps) ToolResultMessage(sessionID string, message llm.LLMMessage) storage.Message {
	return toolResultStorageMessage(sessionID, message)
}

func (d agentToolRunDeps) ToolCallMessage(sessionID, content, rawText string, calls []llm.ToolCallRequest) storage.Message {
	return toolCallStorageMessage(sessionID, content, rawText, calls)
}

func (d agentToolRunDeps) PersistedToolMessage(message llm.LLMMessage) llm.LLMMessage {
	return persistedToolMessage(message)
}

func (a *Agent) toolRunManager() *toolrun.Manager {
	if a.toolRuntime.manager == nil {
		a.toolRuntime.manager = toolrun.NewManager(a.toolRuntime.registry, a.securityPolicy)
	}
	return a.toolRuntime.manager
}

func (a *Agent) cachedToolsForSession(session *storage.Session) []toolrun.CachedTool {
	if session == nil {
		return nil
	}
	metadata := decodeSessionMetadata(session.Metadata)
	backgroundSession := strings.TrimSpace(metadata.BackgroundKind) != ""
	cached := []toolrun.CachedTool{}
	for _, item := range metadata.ToolCache {
		if backgroundSession && item.Name == "discover_tool" {
			continue
		}
		cached = append(cached, item)
	}
	for _, name := range metadata.DiscoveredTools {
		if backgroundSession && name == "discover_tool" {
			continue
		}
		if a.toolRuntime.registry == nil {
			continue
		}
		if t, ok := a.toolRuntime.registry.Get(name); ok {
			schema := t.Schema()
			cached = append(cached, toolrun.CachedTool{Name: name, Source: toolrun.SourceKindNative, Description: t.Info().Description, Schema: schema})
		}
	}
	return toolrun.NormalizeCachedTools(cached)
}
