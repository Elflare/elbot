package confirmation

import (
	"context"
	"elbot/internal/config"
	"elbot/internal/security"
	"elbot/internal/tool"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/control"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/session"
	"elbot/internal/storage"

	"elbot/internal/turn"
)

// ConfirmationPolicy bounds append and tool confirmation waits using session TTL configuration.
type ConfirmationPolicy struct {
	identity                ConfirmationIdentity
	idleExpiration          config.SessionIdleExpirationConfig
	userConfirmationTimeout time.Duration
}

type ConfirmationCoordinator struct {
	commandText func(string, ...string) string
	sessions    interface {
		CurrentBound(context.Context, session.Scope) (*storage.Session, *session.Binding, error)
		EnterActivation(context.Context, session.Scope, ...string) (context.Context, func(), error)
		RecordActivity(context.Context, *session.Binding) error
	}
	requests interface {
		CancelSession(string) int
	}
	turns interface {
		BeginRiskConfirmation(string, turn.RiskConfirmation, string) *turn.RiskConfirmationWait
		PendingRiskConfirmation(string) (turn.RiskConfirmation, bool)
		RefreshRiskConfirmation(string) bool
		ResolveRiskConfirmation(string, turn.RiskDecision) bool
		StopSession(string)
		Snapshot(string) turn.Snapshot
	}
	identity           ConfirmationIdentity
	output             ConfirmationOutput
	policy             agentshare.ConfirmationWaitPolicy
	autoConfirmMu      sync.Mutex
	autoConfirmSession map[string]bool
	autoConfirmTools   map[string]map[string]bool
}

func (p *ConfirmationPolicy) WaitTimeout(ctx context.Context) time.Duration {
	actor := p.identity.Actor(ctx)
	isSuperadmin := actor.Role == contextinfo.RoleSuperadmin
	cfg := p.idleExpiration
	ttlMinutes := cfg.PrivateUserTTLMinutes
	if isSuperadmin {
		ttlMinutes = cfg.PrivateSuperadminTTLMinutes
	}
	scope := p.identity.Scope(ctx)
	if scope.ChatType == "group" || scope.ChatType == "channel" {
		ttlMinutes = cfg.GroupUserTTLMinutes
		if isSuperadmin {
			ttlMinutes = cfg.GroupSuperadminTTLMinutes
		}
	}
	var sessionTimeout time.Duration
	if ttlMinutes > 0 {
		sessionTimeout = time.Duration(ttlMinutes) * time.Minute
	}
	if isSuperadmin {
		return sessionTimeout
	}
	userTimeout := p.userConfirmationTimeout
	if sessionTimeout > 0 && sessionTimeout < userTimeout {
		return sessionTimeout
	}
	return userTimeout
}

// Current 读取原等待快照；显式刷新只作用于同一风险等待。
func (c *ConfirmationCoordinator) Current(ctx context.Context, req control.CurrentConfirmationRequest) (control.CurrentConfirmationResult, error) {
	ctx, row, release, err := c.confirmationContext(ctx)
	if err != nil {
		return control.CurrentConfirmationResult{}, err
	}
	if row == nil {
		return control.CurrentConfirmationResult{}, nil
	}
	defer func() { release() }()
	snapshot := c.turns.Snapshot(row.ID)
	if snapshot.Phase == turn.PhaseAwaitAppendConfirm {
		return control.CurrentConfirmationResult{SessionID: row.ID, Kind: "append"}, nil
	}
	pending, ok := c.turns.PendingRiskConfirmation(row.ID)
	if !ok {
		return control.CurrentConfirmationResult{}, nil
	}
	if req.Refresh {
		if !c.turns.RefreshRiskConfirmation(row.ID) {
			return control.CurrentConfirmationResult{}, nil
		}
		binding, _ := session.BindingFromContext(ctx)
		if err := c.sessions.RecordActivity(ctx, binding); err != nil {
			return control.CurrentConfirmationResult{}, err
		}
	}
	return control.CurrentConfirmationResult{SessionID: row.ID, Kind: "tool", ToolName: pending.ToolName, Arguments: pending.Arguments, Risk: pending.Risk, Detail: pending.Detail}, nil
}

// Respond 只消费类型化决定，并作用于当前绑定 session 的等待项。
func (c *ConfirmationCoordinator) Respond(ctx context.Context, req control.RespondConfirmationRequest) (control.RespondConfirmationResult, error) {
	ctx, row, release, err := c.confirmationContext(ctx)
	if err != nil {
		return control.RespondConfirmationResult{}, err
	}
	if row == nil {
		return control.RespondConfirmationResult{}, nil
	}
	defer func() { release() }()
	snapshot := c.turns.Snapshot(row.ID)
	if snapshot.Phase == turn.PhaseAwaitAppendConfirm {
		if req.Decision != "approve" || req.Scope != "current" {
			return control.RespondConfirmationResult{}, nil
		}
		return control.RespondConfirmationResult{Accepted: true, Continuation: &control.Continuation{Kind: "append", SessionID: row.ID}}, nil
	}
	pending, ok := c.turns.PendingRiskConfirmation(row.ID)
	if !ok {
		return control.RespondConfirmationResult{}, nil
	}
	decision := turn.RiskDecision{Action: turn.RiskConfirm, Extra: req.Extra}
	action := "confirm"
	if req.Decision == "reject" {
		decision = turn.RiskDecision{Action: turn.RiskReject, Reason: req.Extra}
		action = "reject"
	} else {
		switch req.Scope {
		case "tool":
			decision.Action = turn.RiskConfirmTool
			action = "confirmtool"
		case "all":
			decision.Action = turn.RiskConfirmAll
			action = "confirmall"
		}
	}
	binding, _ := session.BindingFromContext(ctx)
	if err := c.sessions.RecordActivity(ctx, binding); err != nil {
		return control.RespondConfirmationResult{}, err
	}
	if !c.turns.ResolveRiskConfirmation(row.ID, decision) {
		return control.RespondConfirmationResult{}, nil
	}
	observed := confirmationAction(ctx, row.ID, action, pending, req.Extra)
	release()
	release = func() {}
	events.Log(context.WithoutCancel(ctx), *observed)
	return control.RespondConfirmationResult{Accepted: true}, nil
}

func (c *ConfirmationCoordinator) confirmationContext(ctx context.Context) (context.Context, *storage.Session, func(), error) {
	row, _, err := c.sessions.CurrentBound(ctx, c.identity.Scope(ctx))
	if errors.Is(err, session.ErrNoCurrentSession) {
		return ctx, nil, nil, nil
	}
	if err != nil {
		return ctx, nil, nil, err
	}
	locked, release, err := c.sessions.EnterActivation(ctx, c.identity.Scope(ctx), row.ID)
	if err != nil {
		return ctx, nil, nil, err
	}
	locked, err = agentshare.CaptureSessionBinding(locked, c.sessions, c.identity, row)
	if err != nil {
		release()
		return ctx, nil, nil, err
	}
	return locked, row, release, nil
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
	record := &events.LogRecord{
		Category:     events.LogAudit,
		Level:        slog.LevelInfo,
		EventName:    "risk_confirmation_command",
		Module:       "agent",
		Type:         events.ContentJSON,
		Content:      string(data),
		ContentLimit: 0,
		ResultStatus: events.ResultUnspecified,
	}
	record.Identity = events.LogIdentityFromContext(ctx)
	record.Identity.SessionID = sessionID
	return record
}

func (c *ConfirmationCoordinator) isSessionAutoConfirmed(sessionID string) bool {
	c.autoConfirmMu.Lock()
	defer c.autoConfirmMu.Unlock()
	return c.autoConfirmSession[sessionID]
}

func (c *ConfirmationCoordinator) setSessionAutoConfirmed(sessionID string) {
	c.autoConfirmMu.Lock()
	defer c.autoConfirmMu.Unlock()
	c.autoConfirmSession[sessionID] = true
}

func (c *ConfirmationCoordinator) isToolAutoConfirmed(sessionID, toolName string) bool {
	c.autoConfirmMu.Lock()
	defer c.autoConfirmMu.Unlock()
	return c.autoConfirmTools[sessionID][toolName]
}

func (c *ConfirmationCoordinator) setToolAutoConfirmed(sessionID, toolName string) {
	c.autoConfirmMu.Lock()
	defer c.autoConfirmMu.Unlock()
	if c.autoConfirmTools[sessionID] == nil {
		c.autoConfirmTools[sessionID] = map[string]bool{}
	}
	c.autoConfirmTools[sessionID][toolName] = true
}

func (c *ConfirmationCoordinator) publishConfirmationWait(ctx context.Context, sessionID string, call llm.ToolCallRequest, risk security.RiskLevel, reasons []string) {
	content := map[string]string{"tool": call.Name, "risk": string(risk), "arguments": call.Arguments, "risk_reasons": strings.Join(reasons, "; ")}
	for key, value := range content {
		runes := []rune(value)
		if len(runes) > 2048 {
			content[key] = string(runes[:2047]) + "…"
		}
	}
	data, _ := json.Marshal(content)
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	facts.SessionID = sessionID
	agentshare.EmitAgentLog(ctx, facts, events.LogRecord{
		Category:     events.LogAudit,
		Level:        slog.LevelInfo,
		EventName:    "risk_confirmation_wait",
		Module:       "agent",
		Type:         events.ContentJSON,
		Content:      string(data),
		ContentLimit: 0,
		Identity:     events.LogIdentity{},
		ResultStatus: events.ResultUnspecified,
	})
}

func (c *ConfirmationCoordinator) publishConfirmationResult(ctx context.Context, sessionID string, call llm.ToolCallRequest, risk security.RiskLevel, action, extra, reason string, resultStatus events.ResultStatus, level slog.Level) {
	content := map[string]string{"tool": call.Name, "risk": string(risk), "action": action, "extra": extra, "reason": reason}
	for key, value := range content {
		runes := []rune(value)
		if len(runes) > 2048 {
			content[key] = string(runes[:2047]) + "…"
		}
	}
	data, _ := json.Marshal(content)
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	facts.SessionID = sessionID
	agentshare.EmitAgentLog(ctx, facts, events.LogRecord{
		Category:     events.LogAudit,
		Level:        level,
		EventName:    "risk_confirmation_result",
		Module:       "agent",
		Type:         events.ContentJSON,
		Content:      string(data),
		ContentLimit: 0,
		Identity:     events.LogIdentity{},
		ResultStatus: resultStatus,
	})
}

func (c *ConfirmationCoordinator) AwaitToolConfirmation(ctx context.Context, sessionID string, call llm.ToolCallRequest, assessment tool.RiskAssessment, detail string) (tool.ConfirmResult, error) {
	if c.isSessionAutoConfirmed(sessionID) || c.isToolAutoConfirmed(sessionID, call.Name) {
		return tool.ConfirmResult{Allowed: true}, nil
	}
	fullArgs := compactArguments(call.Arguments)
	previewArgs := previewArguments(fullArgs)
	timeout := c.policy.WaitTimeout(ctx)
	wait := c.turns.BeginRiskConfirmation(sessionID, turn.RiskConfirmation{
		ToolName:  call.Name,
		Arguments: fullArgs,
		Risk:      string(assessment.Level),
		Detail:    detail,
	}, turn.AttemptFromContext(ctx))
	defer wait.Cancel()
	if wait != nil && ctx.Err() == nil {
		c.publishConfirmationWait(ctx, sessionID, call, assessment.Level, assessment.Reasons)
		presentation := riskConfirmationActions(c.commandText)
		if _, err := c.output.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: fmt.Sprintf("%s [%s]: confirm\n%s%s\n\n%s", call.Name, assessment.Level, previewArgs, riskReasonsText(assessment.Reasons), riskConfirmationPromptText(timeout, c.commandText)), Actions: presentation.Actions}); err != nil {
			resultStatus, level := events.ResultFailed, slog.LevelWarn
			if events.LogStatus(err) == events.ResultCanceled {
				resultStatus = events.ResultCanceled
				level = slog.LevelInfo
			}
			c.publishConfirmationResult(ctx, sessionID, call, assessment.Level, "prompt_failed", "", "send risk confirmation:\n"+err.Error(), resultStatus, level)
			return tool.ConfirmResult{Stopped: true}, fmt.Errorf("send risk confirmation: %w", err)
		}
	}
	result := wait.Wait(ctx, timeout)
	resp := result.Decision
	if result.Outcome == turn.RiskExpired {
		c.publishConfirmationResult(ctx, sessionID, call, assessment.Level, "expire", resp.Extra, "confirmation wait expired", events.ResultFailed, slog.LevelInfo)
		_, err := c.output.Send(context.WithoutCancel(ctx), delivery.StreamEvent{Type: delivery.StreamSend, Text: "confirm timeout → stopped"})
		return tool.ConfirmResult{
			Allowed: false,
			Extra:   resp.Extra,
			Message: llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID, Segments: llm.TextSegments(fmt.Sprintf("tool call %s confirmation expired", call.Name))},
			Stopped: true,
		}, err
	}
	if result.Outcome != turn.RiskAnswered {
		resultStatus := events.ResultSkipped
		if result.Outcome == turn.RiskCanceled {
			resultStatus = events.ResultCanceled
			if result.Err == context.DeadlineExceeded {
				resultStatus = events.ResultFailed
			}
		}
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
		return tool.ConfirmResult{
			Allowed: false,
			Extra:   resp.Extra,
			Message: llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID, Segments: llm.TextSegments(fmt.Sprintf("tool call %s stopped by user", call.Name))},
			Stopped: true,
		}, nil
	}
	if resp.Action == turn.RiskConfirmTool {
		c.setToolAutoConfirmed(sessionID, call.Name)
	}
	if resp.Action == turn.RiskConfirmAll {
		c.setSessionAutoConfirmed(sessionID)
	}
	if resp.Action == turn.RiskReject {
		reason := strings.TrimSpace(resp.Reason)
		if reason == "" {
			reason = "user rejected"
		}
		c.publishConfirmationResult(ctx, sessionID, call, assessment.Level, "reject", resp.Extra, reason, events.ResultRejected, slog.LevelInfo)
		return tool.ConfirmResult{
			Allowed: false,
			Extra:   resp.Extra,
			Message: llm.LLMMessage{
				Role:       llm.RoleTool,
				Name:       call.Name,
				ToolCallID: call.ID,
				Segments:   llm.TextSegments(fmt.Sprintf("tool call %s rejected by user: %s", call.Name, reason)),
			},
		}, nil
	}
	action := "confirm"
	if resp.Action == turn.RiskConfirmTool {
		action = "confirmtool"
	}
	if resp.Action == turn.RiskConfirmAll {
		action = "confirmall"
	}
	c.publishConfirmationResult(ctx, sessionID, call, assessment.Level, action, resp.Extra, "", events.ResultSucceeded, slog.LevelInfo)
	return tool.ConfirmResult{Allowed: true, Extra: resp.Extra}, nil
}

// NewConfirmationCoordinator initializes ConfirmationCoordinator without installing cross-service callbacks.
func NewConfirmationCoordinator(deps ConfirmationCoordinatorDeps) *ConfirmationCoordinator {
	return &ConfirmationCoordinator{
		commandText:        deps.CommandText,
		sessions:           deps.Sessions,
		requests:           deps.Requests,
		turns:              deps.Turns,
		identity:           deps.Identity,
		output:             deps.Output,
		policy:             deps.Policy,
		autoConfirmSession: map[string]bool{}, autoConfirmTools: map[string]map[string]bool{},
	}
}

// NewConfirmationPolicy initializes ConfirmationPolicy without installing cross-service callbacks.
func NewConfirmationPolicy(deps ConfirmationPolicyDeps) *ConfirmationPolicy {
	return &ConfirmationPolicy{
		identity:                deps.Identity,
		idleExpiration:          deps.IdleExpiration,
		userConfirmationTimeout: deps.UserConfirmationTimeout,
	}
}

// ForgetSession 只释放已退役会话的内存授权，不改变持久化历史。
func (c *ConfirmationCoordinator) ForgetSession(sessionID string) {
	c.autoConfirmMu.Lock()
	defer c.autoConfirmMu.Unlock()
	delete(c.autoConfirmSession, sessionID)
	delete(c.autoConfirmTools, sessionID)
}

const defaultUserConfirmationTimeout = 10 * time.Minute
