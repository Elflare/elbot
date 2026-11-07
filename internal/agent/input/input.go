package input

import (
	"context"
	"elbot/internal/delivery"
	"elbot/internal/tool"
	"elbot/internal/workspace"
	"errors"
	"fmt"
	"log/slog"
	"strings"

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
	turns    interface{ Snapshot(string) turn.Snapshot }
	identity InputIdentity
	hooks    interface {
		AgentInputPrepared(context.Context, agentshare.Session, agentshare.Message) ([]llm.MessageSegment, error)
	}
	output        InputOutput
	execution     InputExecution
	confirmations InputConfirmations
	preloader     interface {
		PrepareTools(context.Context, []string) (tool.ToolPreload, error)
		PrepareSkills(context.Context, string, []string) (tool.SkillPreload, error)
	}
	toolState interface {
		Commit(context.Context, string, tool.StateUpdate) (tool.StateCommit, error)
	}
}

func compactCommandBlockedText(text string, commandText func(string, ...string) string) string {
	return fmt.Sprintf("%s: compacting · 等待或 %s", text, commandText("stop"))
}

func (c *InputCoordinator) HandleInput(ctx context.Context, text string) (context.Context, error) {
	ctx, session, err := c.execution.ResolveInput(ctx, text)
	if err != nil {
		if errors.Is(err, sessionpkg.ErrSessionBusy) {
			globalevents.Notify(sessionpkg.WithBinding(context.WithoutCancel(ctx), nil), globalevents.NewNotification(ctx, slog.LevelWarn, fmt.Sprintf("%v，请等待完成，或先用 %s 停止任务。", err, c.commandText("stop"))))
			return ctx, nil
		}
		return ctx, err
	}
	return c.handleSessionInput(ctx, session, text)
}

func (c *InputCoordinator) ContinueCommandInput(ctx context.Context, continuation command.Continuation) (context.Context, error) {
	locked, release, err := c.sessions.EnterActivation(ctx, c.identity.Scope(ctx), continuation.SessionID)
	if err != nil {
		return ctx, err
	}
	row, err := c.sessions.Resume(locked, c.identity.Scope(ctx), continuation.SessionID)
	if err == nil {
		_, binding, bindErr := c.sessions.CurrentBound(locked, c.identity.Scope(ctx))
		err = bindErr
		ctx = sessionpkg.WithBinding(locked, binding)
	}
	release()
	if err != nil {
		return ctx, err
	}
	segments := agentshare.ReplaceInboundTextSegments(ctx, continuation.Text)
	ctx = agentshare.WithInboundSegments(ctx, segments)
	return c.handleSessionInput(ctx, row, continuation.Text)
}

func (c *InputCoordinator) handleSessionInput(ctx context.Context, session *storage.Session, text string) (context.Context, error) {
	locked, release, err := c.execution.EnterInput(ctx, session)
	if errors.Is(err, agentshare.ErrInputCompacting) {
		_, err := c.output.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: fmt.Sprintf("%v · 等待或 %s", err, c.commandText("stop"))})
		return ctx, err
	}
	if errors.Is(err, agentshare.ErrInputArchived) {
		_, err := c.output.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: fmt.Sprintf("%v · %s 后可继续", err, c.commandText("unarchive"))})
		return ctx, err
	}
	if err != nil {
		return ctx, err
	}
	ctx = locked
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	facts.Mode = session.Mode
	ctx = contextinfo.WithExecution(ctx, facts)
	release()
	segments, err := c.hooks.AgentInputPrepared(ctx,
		agentshare.Session{ID: session.ID, Mode: session.Mode, Title: session.Title, Status: session.Status},
		agentshare.Message{Role: string(llm.RoleUser), Segments: agentshare.InboundSegments(ctx, text)})
	if err != nil {
		return ctx, err
	}
	ctx = agentshare.WithInboundSegments(ctx, segments)
	text = llm.SegmentsTextOnly(segments)

	snapshot := c.turns.Snapshot(session.ID)
	if snapshot.Phase != turn.PhaseAwaitRiskConfirm {
		directives, skillDirectives, err := c.applyInputDirectives(ctx, session, text)
		if err != nil {
			return ctx, err
		}
		if err := c.notifyToolDirectiveResult(ctx, directives); err != nil {
			return ctx, err
		}
		if err := c.notifySkillDirectiveResult(ctx, skillDirectives); err != nil {
			return ctx, err
		}
		text = skillDirectives.Text
		ctx = agentshare.WithInboundSegments(ctx, agentshare.ReplaceInboundTextSegments(ctx, text))
		input := agentshare.InboundTurnInput(ctx, text)
		input.NeedsElyphRules = input.NeedsElyphRules || directives.NeedsElyphRules || skillDirectives.NeedsElyphRules
		ctx = agentshare.WithInboundTurnInput(ctx, input)
		if strings.TrimSpace(text) == "" && !agentshare.HasInboundNonTextSegment(ctx) {
			if len(directives.Injected) > 0 || len(directives.Existing) > 0 || len(skillDirectives.Skills) > 0 {
				binding, _ := sessionpkg.BindingFromContext(ctx)
				return ctx, c.sessions.RecordActivity(ctx, binding)
			}
			return ctx, nil
		}
	}

	ctx, disposition, err := c.execution.AcceptInput(ctx, session, text)
	if err != nil {
		return ctx, err
	}
	if disposition == agentshare.InputRiskConfirmation {
		result, err := c.confirmations.SubmitResponse(ctx, session.ID, text)
		if err != nil {
			return ctx, err
		}
		event := delivery.StreamEvent{Type: delivery.StreamSend, Text: result.Text}
		if result.Confirmation != nil {
			event.Actions = result.Confirmation.Actions
		}
		_, err = c.output.Send(ctx, event)
		return ctx, err
	}
	return ctx, nil
}

// NewInputCoordinator initializes InputCoordinator without installing cross-service callbacks.
func NewInputCoordinator(deps InputCoordinatorDeps) *InputCoordinator {
	return &InputCoordinator{
		commandText:       deps.CommandText,
		sessions:          deps.Sessions,
		NewWorkspaceStore: deps.NewWorkspaceStore,
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
