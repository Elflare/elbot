package input

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/control"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/operationresult"
	sessionpkg "elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/turn"
	"elbot/internal/workspace"
)

// InputCoordinator prepares input outside admission and delegates execution state
// changes to ExecutionCoordinator and the existing Session/Turn services.
type InputCoordinator struct {
	commandText    func(string, ...string) string
	normalizeInput func(context.Context, string, []llm.MessageSegment) (operationresult.Result[[]llm.MessageSegment], error)
	// NewWorkspaceStore binds workspace operations to the admitted session.
	NewWorkspaceStore func(string) workspace.WorkspaceStore
	sessions          interface {
		EnterActivation(context.Context, sessionpkg.Scope, ...string) (context.Context, func(), error)
		Resume(context.Context, sessionpkg.Scope, string) (operationresult.Result[*storage.Session], error)
		CurrentBound(context.Context, sessionpkg.Scope) (operationresult.Result[sessionpkg.CurrentBoundData], error)
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

func (c *InputCoordinator) CompletionWorkspaceRoot(ctx context.Context) (string, error) {
	ctx = contextinfo.WithActor(ctx, c.identity.Actor(ctx))
	current, err := c.sessions.CurrentBound(ctx, c.identity.Scope(ctx))
	if err != nil {
		return "", err
	}
	switch current.Status {
	case operationresult.NoCurrentSession:
		return workspace.CurrentWorkspaceDir(ctx)
	case operationresult.Success:
		return workspace.CurrentWorkspaceDir(workspace.WithWorkspaceStore(ctx, c.NewWorkspaceStore(current.Data.Session.ID)))
	default:
		return "", fmt.Errorf("%s", current.Message)
	}
}

func (c *InputCoordinator) ToolCompletionContext(ctx context.Context) (context.Context, error) {
	ctx = contextinfo.WithActor(ctx, c.identity.Actor(ctx))
	current, err := c.sessions.CurrentBound(ctx, c.identity.Scope(ctx))
	if err != nil {
		return ctx, err
	}
	switch current.Status {
	case operationresult.NoCurrentSession:
		return c.execution.DefaultModelContext(ctx)
	case operationresult.Success:
		return c.execution.ModelContext(ctx, current.Data.Session)
	default:
		return ctx, fmt.Errorf("%s", current.Message)
	}
}

func compactCommandBlockedText(text string, commandText func(string, ...string) string) string {
	return fmt.Sprintf("%s: compacting · 等待或 %s", text, commandText("stop"))
}

func (c *InputCoordinator) HandleInput(ctx context.Context, text string) (context.Context, error) {
	ctx, result, err := c.execution.ResolveInput(ctx, text)
	if err != nil {
		return ctx, err
	}
	if result.Status != operationresult.Success {
		globalevents.Notify(sessionpkg.WithBinding(context.WithoutCancel(ctx), nil), globalevents.NewNotification(ctx, slog.LevelWarn, result.Message))
		return ctx, nil
	}
	return c.handleSessionInput(ctx, result.Data, text)
}

func (c *InputCoordinator) ContinueCommandInput(ctx context.Context, continuation control.Continuation) (context.Context, operationresult.Result[struct{}], error) {
	if continuation.Kind == "append" {
		return c.execution.ResumeAppend(ctx, continuation.SessionID)
	}
	locked, release, err := c.sessions.EnterActivation(ctx, c.identity.Scope(ctx), continuation.SessionID)
	if err != nil {
		return ctx, operationresult.Result[struct{}]{}, err
	}
	resumed, err := c.sessions.Resume(locked, c.identity.Scope(ctx), continuation.SessionID)
	if err != nil {
		release()
		return ctx, operationresult.Result[struct{}]{}, err
	}
	if resumed.Status != operationresult.Success {
		release()
		return ctx, operationresult.Result[struct{}]{Status: resumed.Status, Message: resumed.Message}, nil
	}
	row := resumed.Data
	current, err := c.sessions.CurrentBound(locked, c.identity.Scope(ctx))
	if err != nil {
		release()
		return ctx, operationresult.Result[struct{}]{}, err
	}
	if current.Status != operationresult.Success {
		release()
		return ctx, operationresult.Result[struct{}]{Status: current.Status, Message: current.Message}, nil
	}
	ctx = sessionpkg.WithBinding(locked, current.Data.Binding)
	release()
	segments := agentshare.ReplaceInboundTextSegments(ctx, continuation.Text)
	ctx = agentshare.WithInboundSegments(ctx, segments)
	next, err := c.handleSessionInput(ctx, row, continuation.Text)
	if err != nil {
		return next, operationresult.Result[struct{}]{}, err
	}
	return next, operationresult.Result[struct{}]{Status: operationresult.Success}, nil
}

func (c *InputCoordinator) handleSessionInput(ctx context.Context, session *storage.Session, text string) (context.Context, error) {
	locked, admitted, release, err := c.execution.EnterInput(ctx, session)
	if err != nil {
		return ctx, err
	}
	if admitted.Status != operationresult.Success {
		_, err := c.output.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: admitted.Message})
		return ctx, err
	}
	ctx = locked
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	facts.Mode = session.Mode
	ctx = contextinfo.WithExecution(ctx, facts)
	release()
	result, err := c.normalizeInput(ctx, session.ID, agentshare.InboundSegments(ctx, text))
	if err != nil {
		return ctx, err
	}
	if result.Status != operationresult.Success {
		_, err := c.output.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: result.Message})
		return ctx, err
	}
	ctx = agentshare.WithInboundSegments(ctx, result.Data)
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
		prepared, skillDirectives, err := c.applyInputDirectives(ctx, session, text)
		if err != nil {
			return ctx, err
		}
		if prepared.Status != operationresult.Success {
			_, err := c.output.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: prepared.Message})
			return ctx, err
		}
		directives := prepared.Data
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
	if disposition.Status != operationresult.Success {
		_, err := c.output.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: disposition.Message})
		return ctx, err
	}
	if disposition.Data == agentshare.InputRiskConfirmation {
		current, err := c.confirmations.CurrentConfirmation(ctx, control.CurrentConfirmationRequest{})
		if err != nil {
			return ctx, err
		}
		if current.Status != operationresult.Success {
			_, err := c.output.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: current.Message})
			return ctx, err
		}
		text := "无待确认"
		if current.Data.Kind == "tool" {
			text = "待确认\n" + c.commandText("detail") + ": 详情 · " + c.commandText("confirm") + ": 确认 · " + c.commandText("reject") + ": 拒绝 · " + c.commandText("stop") + ": 停止"
		}
		_, err = c.output.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: text})
		return ctx, err
	}
	return ctx, nil
}

// NewInputCoordinator initializes InputCoordinator; platform-specific normalization is optional.
func NewInputCoordinator(deps InputCoordinatorDeps) *InputCoordinator {
	normalizeInput := deps.NormalizeInput
	if normalizeInput == nil {
		normalizeInput = func(_ context.Context, _ string, segments []llm.MessageSegment) (operationresult.Result[[]llm.MessageSegment], error) {
			return operationresult.Result[[]llm.MessageSegment]{Status: operationresult.Success, Data: segments}, nil
		}
	}
	return &InputCoordinator{
		commandText:       deps.CommandText,
		normalizeInput:    normalizeInput,
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
