package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	agentevents "elbot/internal/agent/events"
	"elbot/internal/command"
	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
	"elbot/internal/turn"
)

// confirmationPolicy is shared by append and tool confirmations and idle expiry.
type confirmationPolicy struct {
	identity                *identityResolver
	idleExpiration          session.IdleExpirationConfig
	userConfirmationTimeout time.Duration
}

type confirmationCoordinator struct {
	sessions           *session.Service
	requests           *request.Manager
	turns              *turn.Manager
	commands           *command.Router
	identity           *identityResolver
	output             *outputSender
	policy             *confirmationPolicy
	autoConfirmMu      sync.Mutex
	autoConfirmSession map[string]bool
	autoConfirmTools   map[string]map[string]bool
}

func (p *confirmationPolicy) WaitTimeout(ctx context.Context) time.Duration {
	actor := p.identity.Actor(ctx)
	isSuperadmin := actor.Role == contextinfo.RoleSuperadmin
	ttlMinutes := p.idleExpiration.TTLMinutes(p.identity.Scope(ctx), isSuperadmin)
	var sessionTimeout time.Duration
	if ttlMinutes > 0 {
		sessionTimeout = time.Duration(ttlMinutes) * time.Minute
	}
	if isSuperadmin {
		return sessionTimeout
	}
	userTimeout := p.userConfirmationTimeout
	if userTimeout <= 0 {
		userTimeout = defaultUserConfirmationTimeout
	}
	if sessionTimeout > 0 && sessionTimeout < userTimeout {
		return sessionTimeout
	}
	return userTimeout
}

func (c *confirmationCoordinator) SubmitResponse(ctx context.Context, sessionID, text string) error {
	locked, release, err := c.sessions.EnterActivation(ctx, c.identity.Scope(ctx), sessionID)
	if err != nil {
		return err
	}
	locked, err = captureSessionBinding(locked, c.sessions, c.identity, &storage.Session{ID: sessionID})
	if err != nil {
		release()
		return err
	}
	ctx = locked
	var observed *events.LogRecord
	defer func() {
		release()
		if observed != nil {
			_ = events.EmitLog(context.WithoutCancel(ctx), *observed)
		}
	}()
	confirmation, hasConfirmation := c.turns.PendingRiskConfirmation(sessionID)
	if !hasConfirmation {
		release()
		return nil
	}
	// 风险确认等待期间只接受确认/拒绝/详情/停止类命令。
	// 普通文本不能混入当前 turn，避免被误当作高风险工具的隐式确认
	// 或污染下一次 LLM 调用上下文。
	if !c.commands.IsCommand(text) {
		observed = confirmationAction(ctx, sessionID, "invalid_text", confirmation, "")
		release()
		c.output.SendChat(ctx, riskConfirmationWaitingText())
		return nil
	}

	parsed := c.commands.Parse(text)
	switch parsed.Name {
	case "detail", "details":
		if !c.turns.RefreshRiskConfirmation(sessionID) {
			release()
			return nil
		}
		observed = confirmationAction(ctx, sessionID, "detail", confirmation, "")
		release()
		c.output.SendChat(ctx, riskConfirmationDetailText(confirmation))
	case "confirm", "c":
		observed = confirmationAction(ctx, sessionID, "confirm", confirmation, parsed.Args)
		c.turns.ResolveRiskConfirmation(sessionID, turn.RiskConfirmationResponse{Confirmed: true, Extra: parsed.Args})
	case "confirmtool", "ct":
		observed = confirmationAction(ctx, sessionID, "confirmtool", confirmation, parsed.Args)
		c.turns.ResolveRiskConfirmation(sessionID, turn.RiskConfirmationResponse{Confirmed: true, ConfirmTool: true, Extra: parsed.Args})
	case "confirmall", "ca":
		observed = confirmationAction(ctx, sessionID, "confirmall", confirmation, parsed.Args)
		c.turns.ResolveRiskConfirmation(sessionID, turn.RiskConfirmationResponse{Confirmed: true, ConfirmAll: true, Extra: parsed.Args})

	case "reject":
		observed = confirmationAction(ctx, sessionID, "reject", confirmation, parsed.Args)
		c.turns.ResolveRiskConfirmation(sessionID, turn.RiskConfirmationResponse{Rejected: true, Reason: parsed.Args})
	case "stop":
		observed = confirmationAction(ctx, sessionID, "stop", confirmation, "")
		c.requests.CancelSession(sessionID)
		c.turns.ResolveRiskConfirmation(sessionID, turn.RiskConfirmationResponse{Stopped: true})
		release()
		c.output.SendChat(ctx, "stopped")
	default:
		if hasConfirmation {
			observed = confirmationAction(ctx, sessionID, "invalid_command", confirmation, parsed.Name)
		}
		release()
		c.output.SendChat(ctx, riskConfirmationWaitingText())

	}
	release()
	return nil
}

func confirmationAction(ctx context.Context, sessionID, action string, confirmation turn.RiskConfirmation, extra string) *events.LogRecord {
	content := map[string]string{"tool": confirmation.ToolName, "risk": confirmation.Risk, "action": action, "extra": extra}
	for key, value := range content {
		runes := []rune(value)
		if len(runes) > 2048 {
			content[key] = string(runes[:2047]) + "…"
		}
	}
	data, _ := json.Marshal(content)
	record := &events.LogRecord{Category: events.LogAudit, Level: slog.LevelInfo, EventName: "risk_confirmation_command", Module: "agent", Type: events.ContentJSON, Content: string(data), ContentLimit: 0, Identity: events.LogIdentity{SessionID: &sessionID}, ResultStatus: events.ResultUnspecified}
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
	return record
}

func (c *confirmationCoordinator) isSessionAutoConfirmed(sessionID string) bool {
	c.autoConfirmMu.Lock()
	defer c.autoConfirmMu.Unlock()
	return c.autoConfirmSession[sessionID]
}

func (c *confirmationCoordinator) setSessionAutoConfirmed(sessionID string) {
	c.autoConfirmMu.Lock()
	defer c.autoConfirmMu.Unlock()
	c.autoConfirmSession[sessionID] = true
}

func (c *confirmationCoordinator) isToolAutoConfirmed(sessionID, toolName string) bool {
	c.autoConfirmMu.Lock()
	defer c.autoConfirmMu.Unlock()
	return c.autoConfirmTools[sessionID] != nil && c.autoConfirmTools[sessionID][toolName]
}

func (c *confirmationCoordinator) setToolAutoConfirmed(sessionID, toolName string) {
	c.autoConfirmMu.Lock()
	defer c.autoConfirmMu.Unlock()
	if c.autoConfirmTools[sessionID] == nil {
		c.autoConfirmTools[sessionID] = map[string]bool{}
	}
	c.autoConfirmTools[sessionID][toolName] = true
}

func (c *confirmationCoordinator) publishConfirmationWait(ctx context.Context, sessionID string, call llm.ToolCallRequest, risk tool.RiskLevel, reasons []string) {
	content := map[string]string{"tool": call.Name, "risk": string(risk), "arguments": call.Arguments, "risk_reasons": strings.Join(reasons, "; ")}
	for key, value := range content {
		runes := []rune(value)
		if len(runes) > 2048 {
			content[key] = string(runes[:2047]) + "…"
		}
	}
	data, _ := json.Marshal(content)
	emitAgentLog(ctx, agentevents.Meta(ctx, sessionID), events.LogRecord{Category: events.LogAudit, Level: slog.LevelInfo, EventName: "risk_confirmation_wait", Module: "agent", Type: events.ContentJSON, Content: string(data), ContentLimit: 0, Identity: events.LogIdentity{}, ResultStatus: events.ResultUnspecified})
}
func (c *confirmationCoordinator) publishConfirmationResult(ctx context.Context, sessionID string, call llm.ToolCallRequest, risk tool.RiskLevel, action, extra, reason string, resultStatus events.ResultStatus, level slog.Level) {
	content := map[string]string{"tool": call.Name, "risk": string(risk), "action": action, "extra": extra, "reason": reason}
	for key, value := range content {
		runes := []rune(value)
		if len(runes) > 2048 {
			content[key] = string(runes[:2047]) + "…"
		}
	}
	data, _ := json.Marshal(content)
	emitAgentLog(ctx, agentevents.Meta(ctx, sessionID), events.LogRecord{Category: events.LogAudit, Level: level, EventName: "risk_confirmation_result", Module: "agent", Type: events.ContentJSON, Content: string(data), ContentLimit: 0, Identity: events.LogIdentity{}, ResultStatus: resultStatus})
}
func (c *confirmationCoordinator) AwaitToolConfirmation(ctx context.Context, sessionID string, call llm.ToolCallRequest, assessment tool.RiskAssessment, detail string) (toolrun.ConfirmResult, error) {
	if c.isSessionAutoConfirmed(sessionID) || c.isToolAutoConfirmed(sessionID, call.Name) {
		return toolrun.ConfirmResult{Allowed: true}, nil
	}
	fullArgs := compactArguments(call.Arguments)
	previewArgs := previewArguments(fullArgs)
	timeout := c.policy.WaitTimeout(ctx)
	wait := c.turns.BeginRiskConfirmation(sessionID, turn.RiskConfirmation{ID: call.ID, ToolName: call.Name, Arguments: fullArgs, Risk: string(assessment.Level), Summary: fmt.Sprintf("%s %s", call.Name, previewArgs), Detail: detail}, turn.AttemptFromContext(ctx))
	defer wait.Cancel()
	if wait != nil && ctx.Err() == nil {
		c.publishConfirmationWait(ctx, sessionID, call, assessment.Level, assessment.Reasons)
		if _, logResult, err := c.output.SendAssistant(ctx, fmt.Sprintf("高风险工具调用等待确认\n工具：%s\n风险：%s\n参数：%s%s\n%s。", call.Name, assessment.Level, previewArgs, riskReasonsText(assessment.Reasons), riskConfirmationPromptText(timeout))); err != nil {
			resultStatus, level := logResult.ResultStatus, slog.LevelError
			if resultStatus == events.ResultCanceled {
				level = slog.LevelInfo
			}
			c.publishConfirmationResult(ctx, sessionID, call, assessment.Level, "prompt_failed", "", "send risk confirmation:\n"+logResult.Content, resultStatus, level)
			return toolrun.ConfirmResult{Stopped: true}, fmt.Errorf("send risk confirmation: %w", err)
		}
	}
	resp, ok, resultStatus := wait.Wait(ctx, timeout)
	if resp.Expired {
		c.publishConfirmationResult(ctx, sessionID, call, assessment.Level, "expire", resp.Extra, "confirmation wait expired", events.ResultFailed, slog.LevelInfo)
		c.output.SendChat(context.WithoutCancel(ctx), "高风险工具确认已过期，当前处理已停止。")
		return toolrun.ConfirmResult{Allowed: false, Extra: resp.Extra, Message: llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID, Segments: llm.TextSegments(fmt.Sprintf("tool call %s confirmation expired", call.Name))}, Stopped: true}, nil
	}
	if !ok || resp.Stopped {
		action, reason := "skip", "confirmation wait is no longer current"
		switch resultStatus {
		case events.ResultCanceled:
			action, reason = "stop", "confirmation wait stopped"
		case events.ResultFailed:
			action, reason = "timeout", "confirmation wait timed out"
		}
		if err := ctx.Err(); err != nil {
			reason = err.Error()
		}
		c.publishConfirmationResult(ctx, sessionID, call, assessment.Level, action, resp.Extra, reason, resultStatus, slog.LevelInfo)
		return toolrun.ConfirmResult{Allowed: false, Extra: resp.Extra, Message: llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID, Segments: llm.TextSegments(fmt.Sprintf("tool call %s stopped by user", call.Name))}, Stopped: true}, nil
	}
	if resp.ConfirmTool {
		c.setToolAutoConfirmed(sessionID, call.Name)
		c.output.SendChat(ctx, fmt.Sprintf("已为当前 Session 自动确认后续 %s 工具调用。", call.Name))
	}
	if resp.ConfirmAll {
		c.setSessionAutoConfirmed(sessionID)
		c.output.SendChat(ctx, "已为当前 Session 自动确认后续高风险工具调用。")
	}
	if resp.Rejected {
		reason := strings.TrimSpace(resp.Reason)
		if reason == "" {
			reason = "user rejected"
		}
		c.publishConfirmationResult(ctx, sessionID, call, assessment.Level, "reject", resp.Extra, reason, events.ResultRejected, slog.LevelInfo)
		return toolrun.ConfirmResult{Allowed: false, Extra: resp.Extra, Message: llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID, Segments: llm.TextSegments(fmt.Sprintf("tool call %s rejected by user: %s", call.Name, reason))}}, nil
	}
	action := "confirm"
	if resp.ConfirmTool {
		action = "confirmtool"
	}
	if resp.ConfirmAll {
		action = "confirmall"
	}
	c.publishConfirmationResult(ctx, sessionID, call, assessment.Level, action, resp.Extra, "", events.ResultSucceeded, slog.LevelInfo)
	return toolrun.ConfirmResult{Allowed: true, Extra: resp.Extra}, nil
}
