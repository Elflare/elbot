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

func compactCommandBlockedText(command string) string {
	return fmt.Sprintf("正在压缩当前会话，暂不执行 %s。请等待压缩完成，或先使用 /stop 取消。", command)
}

func activeTurnCommandBlockedText() string {
	return "当前会话处理中，暂不支持切换。如有必要，请先使用 /stop 结束当前处理。"
}

func (a *Agent) handleInput(ctx context.Context, text string) error {
	ctx, session, err := a.execution.resolveInput(ctx, text)
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
	locked, release, err := a.execution.enterInput(ctx, session)
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

	disposition, err := a.execution.AcceptInput(ctx, session, text)
	if err != nil {
		return err
	}
	if disposition == inputRiskConfirmation {
		return a.confirmations.SubmitResponse(ctx, session.ID, text)
	}
	return nil
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
		Config:       a.waitPolicy.idleExpiration,
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
