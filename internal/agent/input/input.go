package input

import (
	"context"
	"elbot/internal/tool"
	"elbot/internal/workspace"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/command"
	globalevents "elbot/internal/events"
	"elbot/internal/llm"
	sessionpkg "elbot/internal/session"
	"elbot/internal/storage"

	"elbot/internal/turn"
)

// InputCoordinator prepares input outside admission and delegates execution state
// changes to ExecutionCoordinator and the existing Session/Turn services.
type InputCoordinator struct {
	commandText func(string, ...string) string
	// NewWorkspaceStore binds workspace operations to the admitted session.
	NewWorkspaceStore func(string) workspace.WorkspaceStore
	sessions          interface {
		EnterActivation(context.Context, sessionpkg.Scope, ...string) (context.Context, func(), error)
		Resume(context.Context, sessionpkg.Scope, string) (*storage.Session, error)
		CurrentBound(context.Context, sessionpkg.Scope) (*storage.Session, *sessionpkg.Binding, error)
		Current(context.Context, sessionpkg.Scope) (*storage.Session, error)
		RecordActivity(context.Context, *sessionpkg.Binding) error
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
}

func compactCommandBlockedText(text string, commandText func(string, ...string) string) string {
	return fmt.Sprintf("%s: compacting · 等待或 %s", text, commandText("stop"))
}

func (c *InputCoordinator) HandleInput(ctx context.Context, text string) error {
	ctx, session, err := c.execution.ResolveInput(ctx, text)
	if err != nil {
		if errors.Is(err, sessionpkg.ErrSessionBusy) {
			_, err = globalevents.NotificationRequested.Emit(sessionpkg.WithBinding(context.WithoutCancel(ctx), nil), globalevents.NewNotification(ctx, slog.LevelWarn, fmt.Sprintf("%v，请等待完成，或先用 %s 停止任务。", err, c.commandText("stop"))))
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
	if errors.Is(err, agentshare.ErrInputCompacting) {
		return c.output.SendChat(ctx, fmt.Sprintf("%v · 等待或 %s", err, c.commandText("stop")))
	}
	if errors.Is(err, agentshare.ErrInputArchived) {
		return c.output.SendChat(ctx, fmt.Sprintf("%v · %s 后可继续", err, c.commandText("unarchive")))
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
			if len(directives.Injected) > 0 || len(directives.Existing) > 0 || len(skillDirectives.Skills) > 0 {
				binding, _ := sessionpkg.BindingFromContext(ctx)
				return c.sessions.RecordActivity(ctx, binding)
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

// NewInputCoordinator initializes InputCoordinator without installing cross-service callbacks.
func NewInputCoordinator(deps InputCoordinatorDeps) *InputCoordinator {
	return &InputCoordinator{
		commandText:       deps.CommandText,
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
	}
}
