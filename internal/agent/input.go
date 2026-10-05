package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"elbot/internal/command"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/platform"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

const defaultUserConfirmationTimeout = 10 * time.Minute

var errInputCompacting = errors.New("正在压缩上下文，请稍后再发送。可使用 /stop 取消当前请求。")
var errInputArchived = errors.New("当前会话已归档，不能继续聊天。若要继续，请先使用 /unarchive。")

func (a *Agent) confirmationWaitTimeout(ctx context.Context) time.Duration {
	actor := a.identity.Actor(ctx)
	isSuperadmin := actor.Role == security.RoleSuperadmin
	ttlMinutes := a.idleExpiration.TTLMinutes(a.identity.Scope(ctx), isSuperadmin)
	var sessionTimeout time.Duration
	if ttlMinutes > 0 {
		sessionTimeout = time.Duration(ttlMinutes) * time.Minute
	}
	if isSuperadmin {
		return sessionTimeout
	}
	userTimeout := a.userConfirmationTimeout
	if userTimeout <= 0 {
		userTimeout = defaultUserConfirmationTimeout
	}
	if sessionTimeout > 0 && sessionTimeout < userTimeout {
		return sessionTimeout
	}
	return userTimeout
}

func confirmationWaitDurationText(timeout time.Duration) string {
	if timeout%time.Minute == 0 {
		return fmt.Sprintf("%d 分钟", int(timeout/time.Minute))
	}
	return timeout.String()
}

func appendConfirmPromptText(timeout time.Duration) string {
	text := "已停止当前处理。是否追加这条消息并重新发送？\n发送 $ / 是 / y / yes 确认；发送 取消 / 否 / n / no 放弃。\n也可以继续发送内容，发送完后再确认。"
	if timeout > 0 {
		text += fmt.Sprintf("\n超过 %s 没有继续发送内容或确认，将自动放弃。", confirmationWaitDurationText(timeout))
	}
	return text
}

func (a *Agent) handleAppendConfirmationInput(ctx context.Context, row *storage.Session, text string) error {
	locked, release, err := a.sessions.EnterActivation(ctx, a.identity.Scope(ctx), row.ID)
	if err != nil {
		return err
	}
	locked, err = a.captureSessionBinding(locked, row)
	if err != nil {
		release()
		return err
	}
	switch {
	case turn.IsConfirm(text):
		merged, execution, ok := a.turns.ResumeAppend(row.ID)
		ctx = turn.WithExecution(locked, execution)
		if !ok || (merged.Text == "" && len(merged.Segments) == 0) {
			release()
			return nil
		}
		ctx = withInboundTurnInput(ctx, merged)
		release()
		return a.startChat(ctx, row, merged.Text)
	case turn.IsCancel(text):
		a.turns.CancelAppend(row.ID)
		release()
		a.output.SendChat(ctx, "已取消追加，本轮处理已停止。")
		return nil
	default:
		a.turns.AppendPendingInput(row.ID, inboundTurnInput(ctx, text))
		release()
		return nil
	}
}

func compactCommandBlockedText(command string) string {
	return fmt.Sprintf("正在压缩当前会话，暂不执行 %s。请等待压缩完成，或先使用 /stop 取消。", command)
}

func activeTurnCommandBlockedText() string {
	return "当前会话处理中，暂不支持切换。如有必要，请先使用 /stop 结束当前处理。"
}

func (a *Agent) handleInput(ctx context.Context, text string) error {
	ctx, session, err := a.resolveInput(ctx, text)
	if err != nil {
		return err
	}
	return a.handleSessionInput(ctx, session, text)
}

func (a *Agent) continueCommandInput(ctx context.Context, continuation command.Continuation) error {
	locked, release, err := a.sessions.EnterActivation(ctx, a.identity.Scope(ctx), continuation.SessionID)
	if err != nil {
		return err
	}
	row, err := a.sessions.Resume(locked, a.identity.Scope(ctx), continuation.SessionID)
	if err == nil {
		_, binding, bindErr := a.sessions.CurrentBound(locked, a.identity.Scope(ctx))
		err = bindErr
		ctx = session.WithBinding(locked, binding)
	}
	release()
	if err != nil {
		return err
	}
	segments := replaceInboundTextSegments(ctx, continuation.Text)
	ctx = withInboundSegments(ctx, segments)
	return a.handleSessionInput(ctx, row, continuation.Text)
}

func (a *Agent) handleSessionInput(ctx context.Context, session *storage.Session, text string) error {
	locked, release, err := a.enterInput(ctx, session)
	if errors.Is(err, errInputCompacting) || errors.Is(err, errInputArchived) {
		a.output.SendChat(ctx, err.Error())
		return nil
	}
	if err != nil {
		return err
	}
	ctx = locked
	release()
	event, err := a.hooks.Run(ctx, hook.Event{Point: hook.PointAgentInputPrepared, Session: hookSession(session), Message: hook.MessagePayload{Role: string(llm.RoleUser), Segments: inboundSegments(ctx, text)}})
	if err != nil {
		return err
	}
	ctx = withInboundSegments(ctx, event.Message.Segments)
	text = llm.SegmentsTextOnly(event.Message.Segments)

	snapshot := a.turns.Snapshot(session.ID)
	if snapshot.Phase != turn.PhaseAwaitRiskConfirm {
		directives, skillDirectives, err := a.applyInputDirectives(ctx, session, text)
		if err != nil {
			return err
		}
		if len(directives.Injected) > 0 || len(directives.Existing) > 0 || len(directives.Invalid) > 0 {
			a.notifyToolDirectiveResult(ctx, directives)
		}
		if len(skillDirectives.Skills) > 0 || len(skillDirectives.InjectedWrappers) > 0 || len(skillDirectives.ExistingWrappers) > 0 || len(skillDirectives.Invalid) > 0 {
			a.notifySkillDirectiveResult(ctx, skillDirectives)
		}
		text = skillDirectives.Text
		ctx = withInboundSegments(ctx, replaceInboundTextSegments(ctx, text))
		if strings.TrimSpace(text) == "" && !hasInboundNonTextSegment(ctx) {
			if len(directives.Injected) > 0 || len(skillDirectives.InjectedWrappers) > 0 {
				if latest, err := a.store.Sessions().Get(ctx, session.ID); err == nil {
					*session = *latest
				}
			}
			return nil
		}
	}

	locked, release, err = a.enterInput(ctx, session)
	if err != nil {
		return err
	}
	ctx = locked
	snapshot = a.turns.Snapshot(session.ID)
	switch snapshot.Phase {
	case turn.PhaseAwaitRiskConfirm:
		release()
		return a.handleRiskConfirmationInput(ctx, session.ID, text)
	case turn.PhaseAwaitAppendConfirm:
		release()
		return a.handleAppendConfirmationInput(ctx, session, text)
	case turn.PhaseLLM:
		if !a.turns.InterruptLLMInput(session.ID, inboundTurnInput(ctx, text)) {
			release()
			return nil
		}
		a.requests.CancelSession(session.ID)
		release()
		timeout := a.confirmationWaitTimeout(ctx)
		a.output.SendChat(ctx, appendConfirmPromptText(timeout))
		if timeout > 0 {
			waitCtx := context.WithoutCancel(ctx)
			go func() {
				if a.turns.AwaitAppendExpiration(session.ID, timeout) {
					a.output.SendChat(waitCtx, "追加确认已过期，待追加内容已丢弃，本轮处理已停止。")
				}
			}()
		}
		return nil
	case turn.PhaseTool:
		a.turns.AppendPendingInput(session.ID, inboundTurnInput(ctx, text))
		release()
		a.output.SendChat(ctx, "已追加，将在当前流程下一次模型调用时带上。发送 /stop 可打断当前流程。")
		return nil
	default:
		release()
		return a.startChat(ctx, session, text)
	}
}

// Preparation runs outside admission. Both entry and commit validate the same
// activation and mode so an old input cannot write into a newly resumed session.
func (a *Agent) enterInput(ctx context.Context, row *storage.Session) (context.Context, func(), error) {
	locked, release, err := a.sessions.EnterActivation(ctx, a.identity.Scope(ctx), row.ID)
	if err != nil {
		return ctx, nil, err
	}
	locked, err = a.captureSessionBinding(locked, row)
	if err == nil {
		var latest *storage.Session
		latest, err = a.store.Sessions().Get(locked, row.ID)
		if err == nil {
			switch {
			case latest.Mode != row.Mode:
				err = errors.New("当前会话模式已切换，请重新发送消息")
			case latest.ArchivedAt != nil:
				err = errInputArchived
			case a.compactActive(row.ID):
				err = errInputCompacting
			}
		}
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		release()
		return ctx, nil, err
	}
	return locked, release, nil
}

func (a *Agent) expireIdleCurrentSession(ctx context.Context) error {
	current, err := a.sessions.Current(ctx, a.identity.Scope(ctx))
	if errors.Is(err, storage.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if a.turns.Snapshot(current.ID).Phase != turn.PhaseIdle {
		return nil
	}
	actor := a.identity.Actor(ctx)
	result, err := a.sessions.ExpireIdleCurrent(ctx, session.ExpireIdleRequest{
		Scope:        a.identity.Scope(ctx),
		IsSuperadmin: actor.Role == security.RoleSuperadmin,
		Config:       a.idleExpiration,
		Now:          time.Now(),
	})
	if err != nil {
		return err
	}
	if result.Expired {
		a.audit("session_idle_expired", "session_id", result.SessionID, "actor_id", actor.ID, "ttl_minutes", result.TTLMinutes)
	}
	return nil
}

func hasForkFromMessage(ctx context.Context) bool {
	msg, ok := platform.MessageContextFrom(ctx)
	return ok && msg.ForkFromMessageID != ""
}

func (a *Agent) sessionForInput(ctx context.Context, text string) (*storage.Session, error) {
	if msg, ok := platform.MessageContextFrom(ctx); ok {
		if msg.ResumeSessionID != "" {
			return a.sessions.Resume(ctx, a.identity.Scope(ctx), msg.ResumeSessionID)
		}
		if msg.ForkFromMessageID != "" {
			return a.sessions.Fork(ctx, a.identity.Scope(ctx), msg.ForkFromMessageID)
		}
	}
	return a.sessions.GetOrCreateCurrent(ctx, a.identity.Scope(ctx), text)
}

func (a *Agent) handleRiskConfirmationInput(ctx context.Context, sessionID, text string) error {
	locked, release, err := a.sessions.EnterActivation(ctx, a.identity.Scope(ctx), sessionID)
	if err != nil {
		return err
	}
	locked, err = a.captureSessionBinding(locked, &storage.Session{ID: sessionID})
	if err != nil {
		release()
		return err
	}
	ctx = locked
	confirmation, hasConfirmation := a.turns.PendingRiskConfirmation(sessionID)
	if !hasConfirmation {
		release()
		return nil
	}
	// 风险确认等待期间只接受确认/拒绝/详情/停止类命令。
	// 普通文本不能混入当前 turn，避免被误当作高风险工具的隐式确认
	// 或污染下一次 LLM 调用上下文。
	if !a.commands.IsCommand(text) {
		a.logRiskConfirmationAction(sessionID, "invalid_text", confirmation, "")
		release()
		a.output.SendChat(ctx, riskConfirmationWaitingText())
		return nil
	}

	parsed := a.commands.Parse(text)
	switch parsed.Name {
	case "detail", "details":
		if !a.turns.RefreshRiskConfirmation(sessionID) {
			release()
			return nil
		}
		a.logRiskConfirmationAction(sessionID, "detail", confirmation, "")
		release()
		a.output.SendChat(ctx, riskConfirmationDetailText(confirmation))
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
		release()
		a.output.SendChat(ctx, "stopped")
	default:
		if hasConfirmation {
			a.logRiskConfirmationAction(sessionID, "invalid_command", confirmation, parsed.Name)
		}
		a.output.SendChat(ctx, riskConfirmationWaitingText())

	}
	release()
	return nil
}

func (a *Agent) logRiskConfirmationAction(sessionID, action string, confirmation turn.RiskConfirmation, extra string) {
	a.audit("risk_confirmation_command", "session_id", sessionID, "action", action, "tool", confirmation.ToolName, "risk", confirmation.Risk, "extra", extra)
}
