package agent

import (
	"context"
	"log/slog"
	"time"

	"elbot/internal/contextmgr"
	"elbot/internal/llm"
	"elbot/internal/modelmgr"
	"elbot/internal/request"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

// AdoptForeground is installed by app as Session's synchronous execution participant.
func (a *Agent) AdoptForeground(ctx context.Context, row *storage.Session, binding *session.Binding) {
	a.execution.AdoptForeground(ctx, row, binding)
}

func (a *Agent) CompactCurrent(ctx context.Context, triggerReason string) (string, error) {
	return a.execution.CompactCurrent(ctx, triggerReason)
}

func (c *executionCoordinator) AdoptForeground(ctx context.Context, row *storage.Session, binding *session.Binding) {
	if execution := c.turns.Execution(row.ID); execution != nil {
		ctx = security.WithActor(ctx, c.identity.Actor(ctx))
		execution.Adopt(session.WithBinding(ctx, binding))
	}
}

// executionCoordinator owns cross-turn admission and handoffs; domain managers
// remain the source of truth for requests, attempts and logical execution state.
type executionCoordinator struct {
	sessions        *session.Service
	sessionRows     storage.SessionRepository
	turns           *turn.Manager
	requests        *request.Manager
	contexts        *contextmgr.Service
	models          *modelmgr.Service
	chat            *chatRunner
	identity        *identityResolver
	view            executionView
	output          *outputSender
	status          *statusRecorder
	waitPolicy      *confirmationPolicy
	responseTimeout time.Duration
	logger          *slog.Logger
	auditLogger     *slog.Logger
}

const foregroundInstructions = "此会话已由用户接管，当前是普通前台对话。保留原任务目标和历史，但后台无人值守、自动汇报及强制 JSON 输出要求已经解除；按当前用户要求正常回复，工具遵循前台权限和确认规则。"

func withForegroundInstructions(messages []llm.LLMMessage) []llm.LLMMessage {
	for _, message := range messages {
		if message.Role == llm.RoleSystem && llm.SegmentsTextOnly(message.Segments) == foregroundInstructions {
			return messages
		}
	}
	return append(messages, llm.LLMMessage{Role: llm.RoleSystem, Segments: llm.TextSegments(foregroundInstructions)})
}

func (c *executionCoordinator) Run(ctx context.Context, row *storage.Session, text string, out turnOutput) error {
	execution := turn.ExecutionFromContext(ctx)
	if execution == nil {
		execution = turn.NewExecution(storage.NewID())
		ctx = turn.WithExecution(ctx, execution)
	}
	out = executionTurnOutput{view: c.view, sessions: c.sessions, foreground: foregroundTurnOutput{sender: c.output, status: c.status}, execution: execution, fallback: out}
	for {
		next, pending, err := c.runAttempt(ctx, row, text, out)
		if err != nil {
			execution.Finish(err)
			return err
		}
		if pending.Text == "" && len(pending.Segments) == 0 {
			if c.turns.Execution(next.ID) != execution {
				execution.Finish(nil)
			}
			return nil
		}
		ctx = c.view.Context(ctx)
		if next.ID != row.ID && !isBackgroundSession(next) {
			_, binding, err := c.sessions.CurrentBound(ctx, c.identity.Scope(ctx))
			if err != nil {
				execution.Finish(err)
				return err
			}
			if binding.SessionID() != next.ID {
				execution.Finish(errSessionBindingChanged)
				return errSessionBindingChanged
			}
			ctx = session.WithBinding(ctx, binding)
		}
		row = next
		text = pending.Text
		ctx = withInboundTurnInput(ctx, pending)
	}
}

func (c *executionCoordinator) RunBackground(ctx context.Context, row *storage.Session, text string) turn.Result {
	execution := turn.NewExecution(storage.NewID())
	execution.SetResult(row.ID, "", "")
	ctx = turn.WithExecution(ctx, execution)
	if err := c.Run(ctx, row, text, backgroundTurnOutput{status: c.status}); err != nil {
		execution.Finish(err)
	}
	result := execution.Wait(ctx)
	if latest, err := c.sessionRows.Get(context.WithoutCancel(ctx), row.ID); err == nil && session.WasPromoted(latest) {
		result.TakenOver = true
	}
	if result.Err != nil && ctx.Err() != nil {
		_, release, err := c.sessions.EnterSessions(context.WithoutCancel(ctx), result.SessionID)
		if err == nil {
			if c.turns.Execution(result.SessionID) == execution {
				c.requests.CancelSession(result.SessionID)
				c.turns.StopSession(result.SessionID)
			}
			release()
		}
	}
	return result
}

func (c *executionCoordinator) audit(event string, attrs ...any) {
	writeAudit(c.auditLogger, slog.LevelInfo, event, attrs...)
}
