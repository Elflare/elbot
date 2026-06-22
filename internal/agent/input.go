package agent

import (
	"context"
	"errors"
	"strings"
	"time"

	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/platform"
	"elbot/internal/security"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func (a *Agent) handleInput(ctx context.Context, text string) error {
	if !hasForkFromMessage(ctx) {
		if err := a.expireIdleCurrentSession(ctx); err != nil {
			return err
		}
	}
	session, err := a.sessionForInput(ctx, text)
	if err != nil {
		return err
	}
	event, err := a.runHook(ctx, hook.Event{Point: hook.PointAgentInputPrepared, Session: a.hookSession(session), Message: hook.MessagePayload{Role: string(llm.RoleUser), Segments: inboundSegments(ctx, text)}})
	if err != nil {
		return err
	}
	ctx = withInboundSegments(ctx, event.Message.Segments)
	text = llm.SegmentsTextOnly(event.Message.Segments)

	if session.ArchivedAt != nil {
		a.sendChat(ctx, "当前会话已归档，不能继续聊天。若要继续，请先使用 /unarchive。")
		return nil
	}

	snapshot := a.turns.Snapshot(session.ID)
	if snapshot.Phase != turn.PhaseAwaitRiskConfirm {
		directives := a.applyToolDirectives(ctx, session, text)
		if len(directives.Injected) > 0 || len(directives.Existing) > 0 || len(directives.Invalid) > 0 {
			a.notifyToolDirectiveResult(ctx, directives)
		}
		text = directives.Text
		ctx = withInboundSegments(ctx, replaceInboundTextSegments(ctx, text))
		if strings.TrimSpace(text) == "" && !hasInboundNonTextSegment(ctx) {
			if len(directives.Injected) > 0 {
				if latest, err := a.store.Sessions().Get(ctx, session.ID); err == nil {
					*session = *latest
				}
			}
			return nil
		}
	}

	switch snapshot.Phase {
	case turn.PhaseAwaitRiskConfirm:
		return a.handleRiskConfirmationInput(ctx, session.ID, text)
	case turn.PhaseAwaitAppendConfirm:
		switch {
		case turn.IsConfirm(text):
			merged, ok := a.turns.ConfirmAppend(session.ID)
			if !ok || merged == "" {
				return nil
			}
			ctx = withInboundSegments(ctx, llm.TextSegments(merged))
			return a.startChat(ctx, session, merged)
		case turn.IsCancel(text):
			a.turns.CancelAppend(session.ID)
			a.sendChat(ctx, "已取消追加，本轮处理已停止。")
			return nil
		default:
			a.turns.AppendPending(session.ID, text)
			return nil
		}
	case turn.PhaseLLM:
		a.requests.CancelSession(session.ID)
		a.turns.InterruptLLM(session.ID, text)
		a.sendChat(ctx, "已停止当前处理。是否追加这条消息并重新发送？\n发送 $ / 是 / y / yes 确认；发送 取消 / 否 / n / no 放弃。\n也可以继续发送内容，发送完后再确认。")
		return nil
	case turn.PhaseTool:
		a.turns.AppendPending(session.ID, text)
		a.sendChat(ctx, "已追加，将在当前流程下一次模型调用时带上。发送 /stop 可打断当前流程。")
		return nil
	case turn.PhaseCompact:
		a.sendChat(ctx, "正在压缩上下文，请稍后再发送。可使用 /stop 取消当前请求。")
		return nil
	default:
		return a.startChat(ctx, session, text)
	}
}

func (a *Agent) expireIdleCurrentSession(ctx context.Context) error {
	actor := a.actor(ctx)
	if actor.Role == security.RoleSuperadmin || a.nonSuperadminIdleTTLMinutes <= 0 {
		return nil
	}
	scope := a.scope(ctx)
	session, err := a.sessions.Current(ctx, scope)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil
		}
		return err
	}
	cutoff := time.Now().Add(-time.Duration(a.nonSuperadminIdleTTLMinutes) * time.Minute)
	if !session.UpdatedAt.Before(cutoff) {
		return nil
	}
	if err := a.sessions.Delete(ctx, scope, session.ID); err != nil && !errors.Is(err, storage.ErrNotFound) {
		return err
	}
	a.audit("session_idle_expired", "session_id", session.ID, "actor_id", actor.ID, "ttl_minutes", a.nonSuperadminIdleTTLMinutes)
	return nil
}

func hasForkFromMessage(ctx context.Context) bool {
	msg, ok := platform.MessageContextFrom(ctx)
	return ok && msg.ForkFromMessageID != ""
}

func (a *Agent) sessionForInput(ctx context.Context, text string) (*storage.Session, error) {
	if msg, ok := platform.MessageContextFrom(ctx); ok {
		if msg.ResumeSessionID != "" {
			return a.sessions.Resume(ctx, a.scope(ctx), msg.ResumeSessionID)
		}
		if msg.ForkFromMessageID != "" {
			return a.sessions.Fork(ctx, a.scope(ctx), msg.ForkFromMessageID)
		}
	}
	return a.sessions.GetOrCreateCurrent(ctx, a.scope(ctx), text)
}

func (a *Agent) handleRiskConfirmationInput(ctx context.Context, sessionID, text string) error {
	confirmation, hasConfirmation := a.turns.PendingRiskConfirmation(sessionID)
	// 风险确认等待期间只接受确认/拒绝/详情/停止类命令。
	// 普通文本不能混入当前 turn，避免被误当作高风险工具的隐式确认
	// 或污染下一次 LLM 调用上下文。
	if !a.commands.IsCommand(text) {
		a.logRiskConfirmationAction(sessionID, "invalid_text", confirmation, "")
		a.sendChat(ctx, riskConfirmationWaitingText())
		return nil
	}

	parsed := a.commands.Parse(text)
	switch parsed.Name {
	case "detail", "details":
		a.logRiskConfirmationAction(sessionID, "detail", confirmation, "")
		a.sendChat(ctx, riskConfirmationDetailText(confirmation))
	case "confirm", "c":
		a.logRiskConfirmationAction(sessionID, "confirm", confirmation, parsed.Args)
		a.turns.ResolveRiskConfirmation(sessionID, turn.RiskConfirmationResponse{Confirmed: true, Extra: parsed.Args})
	case "confirmtool", "ct":
		a.logRiskConfirmationAction(sessionID, "confirmtool", confirmation, parsed.Args)
		a.turns.ResolveRiskConfirmation(sessionID, turn.RiskConfirmationResponse{Confirmed: true, ConfirmTool: true, Extra: parsed.Args})
	case "confirmall", "ca":
		a.logRiskConfirmationAction(sessionID, "confirmall", confirmation, parsed.Args)
		a.turns.ResolveRiskConfirmation(sessionID, turn.RiskConfirmationResponse{Confirmed: true, ConfirmAll: true, Extra: parsed.Args})

	case "reject":
		a.logRiskConfirmationAction(sessionID, "reject", confirmation, parsed.Args)
		a.turns.ResolveRiskConfirmation(sessionID, turn.RiskConfirmationResponse{Rejected: true, Reason: parsed.Args})
	case "stop":
		a.logRiskConfirmationAction(sessionID, "stop", confirmation, "")
		a.requests.CancelSession(sessionID)
		a.turns.ResolveRiskConfirmation(sessionID, turn.RiskConfirmationResponse{Stopped: true})
		a.sendChat(ctx, "stopped")
	default:
		if hasConfirmation {
			a.logRiskConfirmationAction(sessionID, "invalid_command", confirmation, parsed.Name)
		}
		a.sendChat(ctx, riskConfirmationWaitingText())

	}
	return nil
}

func (a *Agent) logRiskConfirmationAction(sessionID, action string, confirmation turn.RiskConfirmation, extra string) {
	a.audit("risk_confirmation_command", "session_id", sessionID, "action", action, "tool", confirmation.ToolName, "risk", confirmation.Risk, "extra", extra)
}
