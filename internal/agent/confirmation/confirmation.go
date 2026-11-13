package confirmation

import (
	"context"
	"elbot/internal/config"
	"elbot/internal/security"
	"elbot/internal/tool"
	"encoding/json"
	"fmt"
	"io"
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
	"elbot/internal/operationresult"
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
		CurrentBound(context.Context, session.Scope) (operationresult.Result[session.CurrentBoundData], error)
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
func (c *ConfirmationCoordinator) Current(ctx context.Context, req control.CurrentConfirmationRequest) (operationresult.Result[control.CurrentConfirmationResult], error) {
	ctx, current, release, err := c.confirmationContext(ctx)
	if err != nil {
		return operationresult.Result[control.CurrentConfirmationResult]{}, err
	}
	if current.Status == operationresult.NoCurrentSession {
		return operationresult.Result[control.CurrentConfirmationResult]{Status: operationresult.Success}, nil
	}
	if current.Status != operationresult.Success {
		return operationresult.Result[control.CurrentConfirmationResult]{Status: current.Status, Message: current.Message}, nil
	}
	row := current.Data
	defer func() { release() }()
	snapshot := c.turns.Snapshot(row.ID)
	if snapshot.Phase == turn.PhaseAwaitAppendConfirm {
		return operationresult.Result[control.CurrentConfirmationResult]{Status: operationresult.Success, Data: control.CurrentConfirmationResult{SessionID: row.ID, Kind: "append", Detail: []tool.ConfirmationDetailBlock{}, Actions: AppendConfirmationActions(c.commandText)}}, nil
	}
	pending, ok := c.turns.PendingRiskConfirmation(row.ID)
	if !ok {
		return operationresult.Result[control.CurrentConfirmationResult]{Status: operationresult.Success}, nil
	}
	if req.Refresh {
		if !c.turns.RefreshRiskConfirmation(row.ID) {
			return operationresult.Result[control.CurrentConfirmationResult]{Status: operationresult.Success}, nil
		}
		binding, _ := session.BindingFromContext(ctx)
		if err := c.sessions.RecordActivity(ctx, binding); err != nil {
			return operationresult.Result[control.CurrentConfirmationResult]{}, err
		}
	}
	detail := []tool.ConfirmationDetailBlock{{Kind: tool.ConfirmationDetailText, Content: fmt.Sprintf("%s [%s]: confirm", pending.ToolName, pending.Risk)}}
	detail = append(detail, fromTurnConfirmationDetails(pending.Detail)...)
	if reasons := riskReasonsText(pending.Reasons); reasons != "" {
		detail = append(detail, tool.ConfirmationDetailBlock{Kind: tool.ConfirmationDetailText, Content: reasons})
	}
	return operationresult.Result[control.CurrentConfirmationResult]{Status: operationresult.Success, Data: control.CurrentConfirmationResult{SessionID: row.ID, Kind: "tool", ToolName: pending.ToolName, Risk: pending.Risk, Detail: detail, Actions: riskConfirmationActions(c.commandText).Actions}}, nil
}

// Respond 只消费类型化决定，并作用于当前绑定 session 的等待项。
func (c *ConfirmationCoordinator) Respond(ctx context.Context, req control.RespondConfirmationRequest) (operationresult.Result[control.RespondConfirmationResult], error) {
	ctx, current, release, err := c.confirmationContext(ctx)
	if err != nil {
		return operationresult.Result[control.RespondConfirmationResult]{}, err
	}
	if current.Status != operationresult.Success {
		return operationresult.Result[control.RespondConfirmationResult]{Status: current.Status, Message: current.Message}, nil
	}
	row := current.Data
	defer func() { release() }()
	snapshot := c.turns.Snapshot(row.ID)
	if snapshot.Phase == turn.PhaseAwaitAppendConfirm {
		if req.Decision != "approve" || req.Scope != "current" {
			return operationresult.Result[control.RespondConfirmationResult]{Status: operationresult.Incompatible, Message: "该确认项只支持确认当前追加输入"}, nil
		}
		return operationresult.Result[control.RespondConfirmationResult]{Status: operationresult.Success, Data: control.RespondConfirmationResult{Accepted: true, Continuation: &control.Continuation{Kind: "append", SessionID: row.ID}}}, nil
	}
	pending, ok := c.turns.PendingRiskConfirmation(row.ID)
	if !ok {
		return operationresult.Result[control.RespondConfirmationResult]{Status: operationresult.NotFound, Message: "当前没有待确认操作"}, nil
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
		return operationresult.Result[control.RespondConfirmationResult]{}, err
	}
	if !c.turns.ResolveRiskConfirmation(row.ID, decision) {
		return operationresult.Result[control.RespondConfirmationResult]{Status: operationresult.Conflict, Message: "待确认操作已变化，请刷新后重试"}, nil
	}
	observed := confirmationAction(ctx, row.ID, action, pending, req.Extra)
	release()
	release = func() {}
	events.Log(context.WithoutCancel(ctx), *observed)
	return operationresult.Result[control.RespondConfirmationResult]{Status: operationresult.Success, Data: control.RespondConfirmationResult{Accepted: true}}, nil
}

func (c *ConfirmationCoordinator) confirmationContext(ctx context.Context) (context.Context, operationresult.Result[*storage.Session], func(), error) {
	current, err := c.sessions.CurrentBound(ctx, c.identity.Scope(ctx))
	if err != nil {
		return ctx, operationresult.Result[*storage.Session]{}, nil, err
	}
	if current.Status != operationresult.Success {
		return ctx, operationresult.Result[*storage.Session]{Status: current.Status, Message: current.Message}, nil, nil
	}
	row := current.Data.Session
	locked, release, err := c.sessions.EnterActivation(ctx, c.identity.Scope(ctx), row.ID)
	if err != nil {
		return ctx, operationresult.Result[*storage.Session]{}, nil, err
	}
	locked, captured, err := agentshare.CaptureSessionBinding(locked, c.sessions, c.identity, row)
	if err != nil {
		release()
		return ctx, operationresult.Result[*storage.Session]{}, nil, err
	}
	if captured.Status != operationresult.Success {
		release()
		return ctx, operationresult.Result[*storage.Session]{Status: captured.Status, Message: captured.Message}, nil, nil
	}
	return locked, operationresult.Result[*storage.Session]{Status: operationresult.Success, Data: row}, release, nil
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
		Category:     events.LogCategoryAudit,
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
		Category:     events.LogCategoryAudit,
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
		Category:     events.LogCategoryAudit,
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

func (c *ConfirmationCoordinator) AwaitToolConfirmation(ctx context.Context, sessionID string, call llm.ToolCallRequest, assessment tool.RiskAssessment, detail []tool.ConfirmationDetailBlock) (operationresult.Result[tool.ConfirmResult], error) {
	if c.isSessionAutoConfirmed(sessionID) || c.isToolAutoConfirmed(sessionID, call.Name) {
		return operationresult.Result[tool.ConfirmResult]{Status: operationresult.Success, Data: tool.ConfirmResult{}}, nil
	}
	fullArgs := compactArguments(call.Arguments)
	detail, err := confirmationDetailsWithArguments(detail, fullArgs)
	if err != nil {
		return operationresult.Result[tool.ConfirmResult]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	timeout := c.policy.WaitTimeout(ctx)
	wait := c.turns.BeginRiskConfirmation(sessionID, turn.RiskConfirmation{
		ToolName: call.Name,
		Risk:     string(assessment.Level),
		Reasons:  append([]string(nil), assessment.Reasons...),
		Detail:   toTurnConfirmationDetails(detail),
	}, turn.AttemptFromContext(ctx))
	if wait == nil {
		c.publishConfirmationResult(ctx, sessionID, call, assessment.Level, "skip", "", "confirmation wait is no longer current", events.ResultSkipped, slog.LevelInfo)
		return operationresult.Result[tool.ConfirmResult]{Status: operationresult.Conflict, Message: "确认等待已变化，请重试"}, nil
	}
	defer wait.Cancel()
	if ctx.Err() == nil {
		c.publishConfirmationWait(ctx, sessionID, call, assessment.Level, assessment.Reasons)
		presentation := riskConfirmationActions(c.commandText)
		blocks := []delivery.ContentBlock{{Kind: delivery.ContentText, Content: fmt.Sprintf("%s [%s]: confirm", call.Name, assessment.Level)}}
		blocks = append(blocks, detail...)
		if reasons := riskReasonsText(assessment.Reasons); reasons != "" {
			blocks = append(blocks, delivery.ContentBlock{Kind: delivery.ContentText, Content: reasons})
		}
		sent, err := c.output.SendTo(ctx, delivery.Target{}, []delivery.Output{{Kind: delivery.KindText, Blocks: blocks, Actions: presentation.Actions}})
		if err != nil {
			resultStatus, level := events.ResultFailed, slog.LevelWarn
			if events.LogStatus(err) == events.ResultCanceled {
				resultStatus = events.ResultCanceled
				level = slog.LevelInfo
			}
			c.publishConfirmationResult(ctx, sessionID, call, assessment.Level, "prompt_failed", "", "send risk confirmation:\n"+err.Error(), resultStatus, level)
			return operationresult.Result[tool.ConfirmResult]{}, fmt.Errorf("send risk confirmation: %w", err)
		}
		if sent.Status != operationresult.Success {
			c.publishConfirmationResult(ctx, sessionID, call, assessment.Level, "prompt_rejected", "", sent.Message, events.ResultRejected, slog.LevelWarn)
			return operationresult.Result[tool.ConfirmResult]{Status: sent.Status, Message: sent.Message}, nil
		}
	}
	result := wait.Wait(ctx, timeout)
	resp := result.Decision
	if result.Outcome == turn.RiskExpired || result.Err == context.DeadlineExceeded {
		c.publishConfirmationResult(ctx, sessionID, call, assessment.Level, "expire", resp.Extra, "confirmation wait expired", events.ResultFailed, slog.LevelInfo)
		sent, err := c.output.SendTo(context.WithoutCancel(ctx), delivery.Target{}, []delivery.Output{delivery.Text("confirm timeout → stopped")})
		if err != nil {
			return operationresult.Result[tool.ConfirmResult]{}, err
		}
		if sent.Status != operationresult.Success {
			return operationresult.Result[tool.ConfirmResult]{Status: sent.Status, Message: sent.Message}, nil
		}
		return operationresult.Result[tool.ConfirmResult]{Status: operationresult.Timeout, Message: "确认等待已超时"}, nil
	}
	if result.Outcome != turn.RiskAnswered {
		if result.Outcome == turn.RiskCanceled {
			reason := "confirmation wait stopped"
			if err := ctx.Err(); err != nil {
				reason = err.Error()
			}
			c.publishConfirmationResult(ctx, sessionID, call, assessment.Level, "stop", resp.Extra, reason, events.ResultCanceled, slog.LevelInfo)
			return operationresult.Result[tool.ConfirmResult]{Status: operationresult.Canceled, Message: "确认已取消"}, nil
		}
		c.publishConfirmationResult(ctx, sessionID, call, assessment.Level, "skip", resp.Extra, "confirmation wait is no longer current", events.ResultSkipped, slog.LevelInfo)
		return operationresult.Result[tool.ConfirmResult]{Status: operationresult.Conflict, Message: "确认等待已失效"}, nil
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
		return operationresult.Result[tool.ConfirmResult]{Status: operationresult.Rejected, Message: fmt.Sprintf("tool call %s rejected by user: %s", call.Name, reason)}, nil
	}
	action := "confirm"
	if resp.Action == turn.RiskConfirmTool {
		action = "confirmtool"
	}
	if resp.Action == turn.RiskConfirmAll {
		action = "confirmall"
	}
	c.publishConfirmationResult(ctx, sessionID, call, assessment.Level, action, resp.Extra, "", events.ResultSucceeded, slog.LevelInfo)
	return operationresult.Result[tool.ConfirmResult]{Status: operationresult.Success, Data: tool.ConfirmResult{Extra: resp.Extra}}, nil
}

func confirmationDetailsWithArguments(details []tool.ConfirmationDetailBlock, argsText string) ([]tool.ConfirmationDetailBlock, error) {
	decoder := json.NewDecoder(strings.NewReader(argsText))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode confirmation arguments: %w", err)
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("confirmation arguments must be a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("confirmation arguments contain trailing data")
	}
	return append(details, tool.ConfirmationDetailBlock{Kind: tool.ConfirmationDetailArguments, Content: value}), nil
}

func toTurnConfirmationDetails(details []tool.ConfirmationDetailBlock) []turn.ConfirmationDetailBlock {
	return append([]turn.ConfirmationDetailBlock(nil), details...)
}

func fromTurnConfirmationDetails(details []turn.ConfirmationDetailBlock) []tool.ConfirmationDetailBlock {
	return append([]tool.ConfirmationDetailBlock(nil), details...)
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
