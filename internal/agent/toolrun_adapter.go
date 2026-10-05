package agent

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/request"
	runtimestatus "elbot/internal/runtime"
	sessionstate "elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
	"elbot/internal/turn"
	"elbot/internal/workspace"
)

type agentToolRunDeps struct {
	agent   *Agent
	output  turnOutput
	attempt string
}

func (d agentToolRunDeps) PrepareToolCall(ctx context.Context, session *storage.Session, call llm.ToolCallRequest) (llm.ToolCallRequest, error) {
	event, err := d.agent.hooks.Run(ctx, hook.Event{
		Point:   hook.PointToolCallPrepared,
		Session: hookSession(session),
		Tool:    hook.ToolPayload{ID: call.ID, Name: call.Name, Arguments: call.Arguments},
	})
	if err != nil {
		return call, err
	}
	call.Arguments = event.Tool.Arguments
	return call, nil
}

func (d agentToolRunDeps) CompleteToolCall(ctx context.Context, session *storage.Session, call llm.ToolCallRequest, risk string, segments []llm.MessageSegment, callErr error) ([]llm.MessageSegment, error) {
	original := append([]llm.MessageSegment(nil), segments...)
	resultText := llm.SegmentsTextOnly(original)
	event, err := d.agent.hooks.Run(ctx, hook.Event{
		Point:   hook.PointToolCallCompleted,
		Session: hookSession(session),
		Message: hook.MessagePayload{Role: string(llm.RoleTool), Segments: original},
		Tool: hook.ToolPayload{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: call.Arguments,
			Risk:      risk,
			Result:    resultText,
			Error:     callErr,
		},
	})
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(event.Message.Segments, original) {
		return d.agent.materializeMedia(ctx, event.Message.Segments), nil
	}
	if event.Tool.Result != resultText {
		return d.agent.materializeMedia(ctx, llm.SetSegmentText(original, event.Tool.Result)), nil
	}
	return d.agent.materializeMedia(ctx, original), nil
}

func (d agentToolRunDeps) StartToolRequest(ctx context.Context, sessionID, toolName string) (context.Context, time.Time, func(), error) {
	toolReq, toolCtx, done, err := d.agent.requests.Start(ctx, request.StartRequest{ParentID: turnRequestIDFromContext(ctx), SessionID: sessionID, Kind: request.KindTool, Label: toolName})
	if err != nil {
		return ctx, time.Time{}, func() {}, err
	}
	d.output.PublishRuntimeStatus(ctx, runtimestatus.Snapshot{SessionID: sessionID, Phase: runtimestatus.PhaseTool, RequestID: toolReq.ID, Kind: request.KindTool, Label: toolName, ToolName: toolName, StageStartedAt: toolReq.StartedAt})
	return toolCtx, toolReq.StartedAt, done, nil
}

func (d agentToolRunDeps) PrepareToolContext(ctx context.Context, session *storage.Session, call llm.ToolCallRequest) context.Context {
	if session == nil {
		return ctx
	}
	state, err := toolrun.DecodeState(session.Metadata)
	if err == nil {
		ctx = tool.WithShownRuleCardFormats(ctx, state.ShownRuleCardFormats)
	}
	ctx = d.agent.fileRollbackContext(ctx, session)
	if isBackgroundSession(session) {
		return ctx
	}
	return workspace.WithWorkspaceStore(ctx, d.agent.workspaceStore(session))
}

func (d agentToolRunDeps) ShouldSendPreview(ctx context.Context, session *storage.Session, call llm.ToolCallRequest, assistantText string) bool {
	return d.agent.identity.IsCLI(ctx) || strings.TrimSpace(assistantText) == ""
}

func (d agentToolRunDeps) ConfirmToolCall(ctx context.Context, sessionID string, call llm.ToolCallRequest, assessment tool.RiskAssessment, detail string) (toolrun.ConfirmResult, error) {
	if d.agent.isSessionAutoConfirmed(sessionID) || d.agent.isToolAutoConfirmed(sessionID, call.Name) {
		return toolrun.ConfirmResult{Allowed: true}, nil
	}
	fullArgs := compactArguments(call.Arguments)
	previewArgs := previewArguments(fullArgs)
	timeout := d.agent.confirmationWaitTimeout(ctx)
	d.agent.logRiskConfirmationWait(sessionID, call, assessment.Level, assessment.Reasons)
	d.agent.output.SendChat(ctx, fmt.Sprintf("高风险工具调用等待确认\n工具：%s\n风险：%s\n参数：%s%s\n%s。", call.Name, assessment.Level, previewArgs, riskReasonsText(assessment.Reasons), riskConfirmationPromptText(timeout)))
	resp, ok := d.agent.turns.AwaitRiskConfirmationContext(ctx, sessionID, turn.RiskConfirmation{ID: call.ID, ToolName: call.Name, Arguments: fullArgs, Risk: string(assessment.Level), Summary: fmt.Sprintf("%s %s", call.Name, previewArgs), Detail: detail}, timeout, turn.AttemptFromContext(ctx))
	if resp.Expired {
		d.agent.logRiskConfirmationResult(sessionID, call, assessment.Level, "expire", resp.Extra, "confirmation wait expired")
		d.agent.output.SendChat(context.WithoutCancel(ctx), "高风险工具确认已过期，当前处理已停止。")
		return toolrun.ConfirmResult{Allowed: false, Extra: resp.Extra, Message: llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID, Segments: llm.TextSegments(fmt.Sprintf("tool call %s confirmation expired", call.Name))}, Stopped: true}, nil
	}
	if !ok || resp.Stopped {
		d.agent.logRiskConfirmationResult(sessionID, call, assessment.Level, "stop", resp.Extra, "")
		return toolrun.ConfirmResult{Allowed: false, Extra: resp.Extra, Message: llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID, Segments: llm.TextSegments(fmt.Sprintf("tool call %s stopped by user", call.Name))}, Stopped: true}, nil
	}
	if resp.ConfirmTool {
		d.agent.setToolAutoConfirmed(sessionID, call.Name)
		d.agent.output.SendChat(ctx, fmt.Sprintf("已为当前 Session 自动确认后续 %s 工具调用。", call.Name))
	}
	if resp.ConfirmAll {
		d.agent.setSessionAutoConfirmed(sessionID)
		d.agent.output.SendChat(ctx, "已为当前 Session 自动确认后续高风险工具调用。")
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
	d.agent.audit("permission_denied", "actor_id", d.agent.identity.Actor(ctx).ID, "session_id", sessionID, "tool", call.Name, "risk", risk, "reason", reason)
}

func (d agentToolRunDeps) RememberDiscoveryResult(ctx context.Context, session *storage.Session, result *tool.Result) error {
	return d.agent.rememberDiscoveryResult(ctx, session, result)
}

func (d agentToolRunDeps) AddToolUse(sessionID, toolName string) {
	d.agent.turns.AddToolUse(sessionID, toolName, d.attempt)
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

func (a *Agent) toolRunManager() *toolrun.Manager { return a.toolRuntime.manager }

func (d agentToolRunDeps) RefreshExecution(ctx context.Context, row *storage.Session) (context.Context, error) {
	return d.agent.view.RefreshSession(ctx, row)
}

func (a *Agent) workspaceStore(row *storage.Session) *sessionstate.WorkspaceStore {
	return sessionstate.NewWorkspaceStore(a.sessions, a.store.Sessions(), row.ID)
}
