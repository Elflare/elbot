package input

import (
	"context"
	"elbot/internal/tool"
	"elbot/internal/workspace"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/command"
	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/llm"
	sessionpkg "elbot/internal/session"
	"elbot/internal/storage"

	"elbot/internal/turn"
)

// InputCoordinator prepares input outside admission and delegates execution state
// changes to ExecutionCoordinator and the existing Session/Turn services.
type InputCoordinator struct {
	// NewWorkspaceStore binds workspace operations to the admitted session.
	NewWorkspaceStore func(string) workspace.WorkspaceStore
	sessions          interface {
		EnterActivation(context.Context, sessionpkg.Scope, ...string) (context.Context, func(), error)
		Resume(context.Context, sessionpkg.Scope, string) (*storage.Session, error)
		CurrentBound(context.Context, sessionpkg.Scope) (*storage.Session, *sessionpkg.Binding, error)
		Current(context.Context, sessionpkg.Scope) (*storage.Session, error)
		ExpireIdleCurrent(context.Context, sessionpkg.ExpireIdleRequest) (sessionpkg.ExpireIdleResult, error)
	}
	sessionRows interface {
		Get(context.Context, string) (*storage.Session, error)
	}
	turns    interface{ Snapshot(string) turn.Snapshot }
	identity InputIdentity
	hooks    interface {
		AgentInputPrepared(context.Context, agentshare.Session, agentshare.Message) ([]llm.MessageSegment, globalevents.LogResult, error)
	}
	output        InputOutput
	execution     InputExecution
	confirmations InputConfirmations
	preloader     interface {
		PrepareTools(context.Context, []string) (tool.ToolPreload, error)
		PrepareSkills(context.Context, string, []string) (tool.SkillPreload, error)
	}
	toolState interface {
		Commit(context.Context, string, tool.StateUpdate) (tool.StateCommit, globalevents.LogResult, error)
	}
	waitPolicy InputWaitPolicy
}

func compactCommandBlockedText(command string) string {
	return fmt.Sprintf("正在压缩当前会话，暂不执行 %s。请等待压缩完成，或先使用 /stop 取消。", command)
}

func (c *InputCoordinator) HandleInput(ctx context.Context, text string) error {
	ctx, session, err := c.execution.ResolveInput(ctx, text)
	if err != nil {
		if errors.Is(err, sessionpkg.ErrSessionBusy) {
			_, err = globalevents.NotificationRequested.Emit(sessionpkg.WithBinding(context.WithoutCancel(ctx), nil), globalevents.NewNotification(ctx, slog.LevelWarn, err.Error()))
		} else if ctx.Err() == nil && !errors.Is(err, agentshare.ErrSessionBindingChanged) {
			agentshare.ReportFailure(ctx, "", fmt.Errorf("准备会话: %w", err), c.output.Notify)
		}
		return err
	}
	return c.handleSessionInput(ctx, session, text)
}

func (c *InputCoordinator) ContinueCommandInput(ctx context.Context, continuation command.Continuation) error {
	locked, release, err := c.sessions.EnterActivation(ctx, c.identity.Scope(ctx), continuation.SessionID)
	if err != nil {
		if ctx.Err() == nil {
			agentshare.ReportFailure(ctx, continuation.SessionID, err, c.output.Notify)
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
			agentshare.ReportFailure(ctx, continuation.SessionID, err, c.output.Notify)
		}
		return err
	}
	segments := agentshare.ReplaceInboundTextSegments(ctx, continuation.Text)
	ctx = agentshare.WithInboundSegments(ctx, segments)
	return c.handleSessionInput(ctx, row, continuation.Text)
}

func (c *InputCoordinator) handleSessionInput(ctx context.Context, session *storage.Session, text string) error {
	locked, release, err := c.execution.EnterInput(ctx, session)
	if errors.Is(err, agentshare.ErrInputCompacting) || errors.Is(err, agentshare.ErrInputArchived) {
		return c.output.SendChat(ctx, err.Error())
	}
	if err != nil {
		if ctx.Err() == nil && !errors.Is(err, agentshare.ErrSessionBindingChanged) {
			agentshare.ReportFailure(ctx, session.ID, err, c.output.Notify)
		}
		return err
	}
	ctx = locked
	release()
	segments, _, err := c.hooks.AgentInputPrepared(ctx,
		agentshare.Session{ID: session.ID, Mode: session.Mode, Title: session.Title, Status: session.Status},
		agentshare.Message{Role: string(llm.RoleUser), Segments: agentshare.InboundSegments(ctx, text)})
	if err != nil {
		if ctx.Err() == nil {
			agentshare.ReportFailure(ctx, session.ID, fmt.Errorf("agent input hook: %w", err), c.output.Notify)
		}
		return err
	}
	ctx = agentshare.WithInboundSegments(ctx, segments)
	text = llm.SegmentsTextOnly(segments)

	snapshot := c.turns.Snapshot(session.ID)
	if snapshot.Phase != turn.PhaseAwaitRiskConfirm {
		directives, skillDirectives, err := c.applyInputDirectives(ctx, session, text)
		if err != nil {
			if ctx.Err() == nil {
				agentshare.ReportFailure(ctx, session.ID, fmt.Errorf("准备输入指令: %w", err), c.output.Notify)
			}
			return err
		}
		if len(directives.Injected) > 0 || len(directives.Existing) > 0 || len(directives.Invalid) > 0 {
			if err := c.notifyToolDirectiveResult(ctx, directives); err != nil {
				if ctx.Err() == nil {
					agentshare.ReportFailure(ctx, session.ID, err, c.output.Notify)
				}
				return err
			}
		}
		if len(skillDirectives.Skills) > 0 || len(skillDirectives.InjectedWrappers) > 0 || len(skillDirectives.ExistingWrappers) > 0 || len(skillDirectives.Invalid) > 0 {
			if err := c.notifySkillDirectiveResult(ctx, skillDirectives); err != nil {
				if ctx.Err() == nil {
					agentshare.ReportFailure(ctx, session.ID, err, c.output.Notify)
				}
				return err
			}
		}
		text = skillDirectives.Text
		ctx = agentshare.WithInboundSegments(ctx, agentshare.ReplaceInboundTextSegments(ctx, text))
		input := agentshare.InboundTurnInput(ctx, text)
		input.NeedsElyphRules = input.NeedsElyphRules || directives.NeedsElyphRules || skillDirectives.NeedsElyphRules
		ctx = agentshare.WithInboundTurnInput(ctx, input)
		if strings.TrimSpace(text) == "" && !agentshare.HasInboundNonTextSegment(ctx) {
			if len(directives.Injected) > 0 || len(skillDirectives.InjectedWrappers) > 0 {
				latest, err := c.sessionRows.Get(ctx, session.ID)
				if err != nil {
					if ctx.Err() == nil {
						agentshare.ReportFailure(ctx, session.ID, err, c.output.Notify)
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
	if disposition == agentshare.InputRiskConfirmation {
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
	msg, ok := agentshare.MessageContextFrom(ctx)
	return ok && msg.ForkFromMessageID != ""
}

// NewInputCoordinator initializes InputCoordinator without installing cross-service callbacks.
func NewInputCoordinator(deps InputCoordinatorDeps) *InputCoordinator {
	return &InputCoordinator{
		sessions:          deps.Sessions,
		NewWorkspaceStore: deps.NewWorkspaceStore,
		sessionRows:       deps.SessionRows,
		turns:             deps.Turns,
		identity:          deps.Identity,
		hooks:             deps.Hooks,
		output:            deps.Output,
		execution:         deps.Execution,
		confirmations:     deps.Confirmations,
		preloader:         deps.Preloader,
		toolState:         deps.ToolState,
		waitPolicy:        deps.WaitPolicy,
	}
}
