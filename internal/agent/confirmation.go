package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	agentevents "elbot/internal/agent/events"
	"elbot/internal/command"
	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/request"
	"elbot/internal/session"
	"elbot/internal/signal"
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
	changed            *signal.Signal[agentevents.ConfirmationChangedEvent]
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
	var observed *agentevents.ConfirmationChangedEvent
	defer func() {
		release()
		if observed != nil {
			agentevents.Emit(ctx, c.changed, *observed)
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

func confirmationAction(ctx context.Context, sessionID, action string, confirmation turn.RiskConfirmation, extra string) *agentevents.ConfirmationChangedEvent {
	return &agentevents.ConfirmationChangedEvent{EventMeta: agentevents.Meta(ctx, sessionID), Phase: "command", Action: action, Tool: confirmation.ToolName, Risk: confirmation.Risk, Extra: extra}
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
	agentevents.Emit(ctx, c.changed, agentevents.ConfirmationChangedEvent{EventMeta: agentevents.Meta(ctx, sessionID), Phase: "wait", Tool: call.Name, Risk: string(risk), Arguments: call.Arguments, Reasons: strings.Join(reasons, "; ")})
}
func (c *confirmationCoordinator) publishConfirmationResult(ctx context.Context, sessionID string, call llm.ToolCallRequest, risk tool.RiskLevel, action, extra, reason string) {
	agentevents.Emit(ctx, c.changed, agentevents.ConfirmationChangedEvent{EventMeta: agentevents.Meta(ctx, sessionID), Phase: "result", Tool: call.Name, Risk: string(risk), Action: action, Extra: extra, Reason: reason})
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
		if _, err := c.output.SendAssistant(ctx, fmt.Sprintf("高风险工具调用等待确认\n工具：%s\n风险：%s\n参数：%s%s\n%s。", call.Name, assessment.Level, previewArgs, riskReasonsText(assessment.Reasons), riskConfirmationPromptText(timeout))); err != nil {
			return toolrun.ConfirmResult{Stopped: true}, fmt.Errorf("send risk confirmation: %w", err)
		}
	}
	resp, ok := wait.Wait(ctx, timeout)
	if resp.Expired {
		c.publishConfirmationResult(ctx, sessionID, call, assessment.Level, "expire", resp.Extra, "confirmation wait expired")
		c.output.SendChat(context.WithoutCancel(ctx), "高风险工具确认已过期，当前处理已停止。")
		return toolrun.ConfirmResult{Allowed: false, Extra: resp.Extra, Message: llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID, Segments: llm.TextSegments(fmt.Sprintf("tool call %s confirmation expired", call.Name))}, Stopped: true}, nil
	}
	if !ok || resp.Stopped {
		c.publishConfirmationResult(ctx, sessionID, call, assessment.Level, "stop", resp.Extra, "")
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
		c.publishConfirmationResult(ctx, sessionID, call, assessment.Level, "reject", resp.Extra, reason)
		return toolrun.ConfirmResult{Allowed: false, Extra: resp.Extra, Message: llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID, Segments: llm.TextSegments(fmt.Sprintf("tool call %s rejected by user: %s", call.Name, reason))}}, nil
	}
	action := "confirm"
	if resp.ConfirmTool {
		action = "confirmtool"
	}
	if resp.ConfirmAll {
		action = "confirmall"
	}
	c.publishConfirmationResult(ctx, sessionID, call, assessment.Level, action, resp.Extra, "")
	return toolrun.ConfirmResult{Allowed: true, Extra: resp.Extra}, nil
}
