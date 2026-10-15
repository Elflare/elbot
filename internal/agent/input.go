package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"elbot/internal/agent/dialogue"
	"elbot/internal/command"
	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/hook"
	"elbot/internal/llm"
	"elbot/internal/platform"
	sessionpkg "elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
	"elbot/internal/turn"
)

const defaultUserConfirmationTimeout = 10 * time.Minute

// InputCoordinator prepares input outside admission and delegates execution state
// changes to ExecutionCoordinator and the existing Session/Turn services.
type InputCoordinator struct {
	sessions      *sessionpkg.Service
	sessionRows   storage.SessionRepository
	turns         *turn.Manager
	identity      InputIdentity
	hooks         dialogue.Hooks
	output        InputOutput
	execution     InputExecution
	confirmations InputConfirmations
	registry      *tool.Registry
	preloader     *toolrun.PreloadService
	toolState     *toolrun.StateService
	waitPolicy    InputWaitPolicy
}

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

func (c *InputCoordinator) HandleInput(ctx context.Context, text string) error {
	ctx, session, err := c.execution.ResolveInput(ctx, text)
	if err != nil {
		if errors.Is(err, sessionpkg.ErrSessionBusy) {
			_, err = globalevents.NotificationRequested.Emit(sessionpkg.WithBinding(context.WithoutCancel(ctx), nil), globalevents.NewNotification(ctx, slog.LevelWarn, err.Error()))
		} else if ctx.Err() == nil && !errors.Is(err, errSessionBindingChanged) {
			dialogue.ReportFailure(ctx, "", fmt.Errorf("准备会话: %w", err), c.output.Notify)
		}
		return err
	}
	return c.handleSessionInput(ctx, session, text)
}

func (c *InputCoordinator) ContinueCommandInput(ctx context.Context, continuation command.Continuation) error {
	locked, release, err := c.sessions.EnterActivation(ctx, c.identity.Scope(ctx), continuation.SessionID)
	if err != nil {
		if ctx.Err() == nil {
			dialogue.ReportFailure(ctx, continuation.SessionID, err, c.output.Notify)
		}
		return err
	}
	row, err := c.sessions.Resume(locked, c.identity.Scope(ctx), continuation.SessionID)
	if err == nil {
		_, binding, bindErr := c.sessions.CurrentBound(locked, c.identity.Scope(ctx))
		err = bindErr
		ctx = sessionpkg.WithBinding(locked, binding)
	}
	release()
	if err != nil {
		if ctx.Err() == nil {
			dialogue.ReportFailure(ctx, continuation.SessionID, err, c.output.Notify)
		}
		return err
	}
	segments := replaceInboundTextSegments(ctx, continuation.Text)
	ctx = withInboundSegments(ctx, segments)
	return c.handleSessionInput(ctx, row, continuation.Text)
}

func (c *InputCoordinator) handleSessionInput(ctx context.Context, session *storage.Session, text string) error {
	locked, release, err := c.execution.EnterInput(ctx, session)
	if errors.Is(err, errInputCompacting) || errors.Is(err, errInputArchived) {
		return c.output.SendChat(ctx, err.Error())
	}
	if err != nil {
		if ctx.Err() == nil && !errors.Is(err, errSessionBindingChanged) {
			dialogue.ReportFailure(ctx, session.ID, err, c.output.Notify)
		}
		return err
	}
	ctx = locked
	release()
	event, _, err := c.hooks.Run(ctx, hook.Event{
		Point:   hook.PointAgentInputPrepared,
		Session: hookSession(session),
		Message: hook.MessagePayload{Role: string(llm.RoleUser), Segments: inboundSegments(ctx, text)},
	})
	if err != nil {
		if ctx.Err() == nil {
			dialogue.ReportFailure(ctx, session.ID, fmt.Errorf("agent input hook: %w", err), c.output.Notify)
		}
		return err
	}
	ctx = withInboundSegments(ctx, event.Message.Segments)
	text = llm.SegmentsTextOnly(event.Message.Segments)

	snapshot := c.turns.Snapshot(session.ID)
	if snapshot.Phase != turn.PhaseAwaitRiskConfirm {
		directives, skillDirectives, err := c.applyInputDirectives(ctx, session, text)
		if err != nil {
			if ctx.Err() == nil {
				dialogue.ReportFailure(ctx, session.ID, fmt.Errorf("准备输入指令: %w", err), c.output.Notify)
			}
			return err
		}
		if len(directives.Injected) > 0 || len(directives.Existing) > 0 || len(directives.Invalid) > 0 {
			if err := c.notifyToolDirectiveResult(ctx, directives); err != nil {
				if ctx.Err() == nil {
					dialogue.ReportFailure(ctx, session.ID, err, c.output.Notify)
				}
				return err
			}
		}
		if len(skillDirectives.Skills) > 0 || len(skillDirectives.InjectedWrappers) > 0 || len(skillDirectives.ExistingWrappers) > 0 || len(skillDirectives.Invalid) > 0 {
			if err := c.notifySkillDirectiveResult(ctx, skillDirectives); err != nil {
				if ctx.Err() == nil {
					dialogue.ReportFailure(ctx, session.ID, err, c.output.Notify)
				}
				return err
			}
		}
		text = skillDirectives.Text
		ctx = withInboundSegments(ctx, replaceInboundTextSegments(ctx, text))
		if strings.TrimSpace(text) == "" && !hasInboundNonTextSegment(ctx) {
			if len(directives.Injected) > 0 || len(skillDirectives.InjectedWrappers) > 0 {
				latest, err := c.sessionRows.Get(ctx, session.ID)
				if err != nil {
					if ctx.Err() == nil {
						dialogue.ReportFailure(ctx, session.ID, err, c.output.Notify)
					}
					return err
				}
				*session = *latest
			}
			return nil
		}
	}

	disposition, err := c.execution.AcceptInput(ctx, session, text)
	if err != nil {
		return err
	}
	if disposition == inputRiskConfirmation {
		return c.confirmations.SubmitResponse(ctx, session.ID, text)
	}
	return nil
}

func (c *InputCoordinator) ExpireIdleCurrentSession(ctx context.Context) error {
	current, err := c.sessions.Current(ctx, c.identity.Scope(ctx))
	if errors.Is(err, storage.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if c.turns.Snapshot(current.ID).Phase != turn.PhaseIdle {
		return nil
	}
	actor := c.identity.Actor(ctx)
	result, err := c.sessions.ExpireIdleCurrent(ctx, sessionpkg.ExpireIdleRequest{
		Scope:        c.identity.Scope(ctx),
		IsSuperadmin: actor.Role == contextinfo.RoleSuperadmin,
		Config:       c.waitPolicy.IdleExpiration(),
		Now:          time.Now(),
	})
	if err != nil {
		return err
	}
	if result.Expired {
		{
			content, _ := json.Marshal(map[string]any{"actor_id": actor.ID, "ttl_minutes": result.TTLMinutes})
			record := globalevents.LogRecord{
				Category:     globalevents.LogAudit,
				Level:        slog.LevelInfo,
				EventName:    "session_idle_expired",
				Module:       "agent",
				Type:         globalevents.ContentJSON,
				Content:      string(content),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultSucceeded,
			}
			facts, _ := contextinfo.ExecutionFromContext(ctx)
			if facts.SessionID != "" {
				record.Identity.SessionID = &facts.SessionID
			}
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
			record.Identity.SessionID = &result.SessionID
			_ = globalevents.EmitLog(ctx, record)
		}
	}
	return nil
}

func hasForkFromMessage(ctx context.Context) bool {
	msg, ok := platform.MessageContextFrom(ctx)
	return ok && msg.ForkFromMessageID != ""
}

// NewInputCoordinator initializes InputCoordinator without installing cross-service callbacks.
func NewInputCoordinator(deps InputCoordinatorDeps) *InputCoordinator {
	return &InputCoordinator{
		sessions:      deps.Sessions,
		sessionRows:   deps.SessionRows,
		turns:         deps.Turns,
		identity:      deps.Identity,
		hooks:         deps.Hooks,
		output:        deps.Output,
		execution:     deps.Execution,
		confirmations: deps.Confirmations,
		registry:      deps.Registry,
		preloader:     deps.Preloader,
		toolState:     deps.ToolState,
		waitPolicy:    deps.WaitPolicy,
	}
}
