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
	"elbot/internal/turn"
	"elbot/internal/workspace"
)

// InputCoordinator prepares input outside admission and delegates execution state
// changes to ExecutionCoordinator and the existing Session/Turn services.
type InputCoordinator struct {
	deps InputCoordinatorDeps
}

func (c *InputCoordinator) CompletionWorkspaceRoot(ctx context.Context) (string, error) {
	current, err := c.deps.Sessions.CurrentBound(ctx, c.deps.Identity.Scope(ctx))
	if err != nil {
		return "", err
	}
	switch current.Status {
	case operationresult.NoCurrentSession:
		return workspace.CurrentWorkspaceDir(ctx)
	case operationresult.Success:
		return workspace.CurrentWorkspaceDir(workspace.WithWorkspaceStore(ctx, c.deps.NewWorkspaceStore(current.Data.Session.ID)))
	default:
		return "", fmt.Errorf("%s", current.Message)
	}
}

func (c *InputCoordinator) ToolCompletionContext(ctx context.Context) (context.Context, error) {
	current, err := c.deps.Sessions.CurrentBound(ctx, c.deps.Identity.Scope(ctx))
	if err != nil {
		return ctx, err
	}
	switch current.Status {
	case operationresult.NoCurrentSession:
		ctx, err = c.deps.Execution.DefaultModelContext(ctx)
	case operationresult.Success:
		ctx, err = c.deps.Execution.ModelContext(ctx, current.Data.Session)
	default:
		return ctx, fmt.Errorf("%s", current.Message)
	}
	if err != nil {
		return ctx, err
	}
	return c.deps.Preloader.PrepareAvailabilityContext(ctx)
}

func compactCommandBlockedText(text string, commandText func(string, ...string) string) string {
	return fmt.Sprintf("%s: compacting · 等待或 %s", text, commandText("stop"))
}

func (c *InputCoordinator) HandleInput(ctx context.Context, text string) (context.Context, error) {
	ctx, result, err := c.deps.Execution.ResolveInput(ctx, text)
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
		return c.deps.Execution.ResumeAppend(ctx, continuation.SessionID)
	}
	resumed, err := func() (operationresult.Result[*storage.Session], error) {
		scope := c.deps.Identity.Scope(ctx)
		locked, release, err := c.deps.Sessions.EnterActivation(ctx, scope, continuation.SessionID)
		if err != nil {
			return operationresult.Result[*storage.Session]{}, err
		}
		defer release()
		resumed, err := c.deps.Sessions.Resume(locked, scope, continuation.SessionID)
		if err != nil || resumed.Status != operationresult.Success {
			return resumed, err
		}
		current, err := c.deps.Sessions.CurrentBound(locked, scope)
		if err != nil || current.Status != operationresult.Success {
			return operationresult.Result[*storage.Session]{Status: current.Status, Message: current.Message}, err
		}
		ctx = sessionpkg.WithBinding(locked, current.Data.Binding)
		return resumed, nil
	}()
	if err != nil {
		return ctx, operationresult.Result[struct{}]{}, err
	}
	if resumed.Status != operationresult.Success {
		return ctx, operationresult.Result[struct{}]{Status: resumed.Status, Message: resumed.Message}, nil
	}
	segments := replaceInboundTextSegments(ctx, continuation.Text)
	ctx = agentshare.WithInboundSegments(ctx, segments)
	next, err := c.handleSessionInput(ctx, resumed.Data, continuation.Text)
	if err != nil {
		return next, operationresult.Result[struct{}]{}, err
	}
	return next, operationresult.Result[struct{}]{Status: operationresult.Success}, nil
}

func (c *InputCoordinator) handleSessionInput(ctx context.Context, session *storage.Session, text string) (context.Context, error) {
	locked, admitted, release, err := c.deps.Execution.EnterInput(ctx, session)
	if err != nil {
		return ctx, err
	}
	if admitted.Status != operationresult.Success {
		_, err := c.deps.Output.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: admitted.Message})
		return ctx, err
	}
	ctx = locked
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	facts.Mode = session.Mode
	ctx = contextinfo.WithExecution(ctx, facts)
	release()
	result, err := c.deps.NormalizeInput(ctx, session.ID, agentshare.InboundSegments(ctx, text))
	if err != nil {
		return ctx, err
	}
	if result.Status != operationresult.Success {
		_, err := c.deps.Output.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: result.Message})
		return ctx, err
	}
	ctx = agentshare.WithInboundSegments(ctx, result.Data)
	segments, err := c.deps.Hooks.AgentInputPrepared(ctx,
		agentshare.Session{ID: session.ID, Mode: session.Mode, Title: session.Title, Status: session.Status},
		agentshare.Message{Role: string(llm.RoleUser), Segments: agentshare.InboundSegments(ctx, text)})
	if err != nil {
		return ctx, err
	}
	ctx = agentshare.WithInboundSegments(ctx, segments)
	text = llm.SegmentsTextOnly(segments)

	snapshot := c.deps.Turns.Snapshot(session.ID)
	if snapshot.Phase != turn.PhaseAwaitRiskConfirm {
		nextCtx, prepared, skillDirectives, err := c.applyInputDirectives(ctx, session, text)
		if err != nil {
			return nextCtx, err
		}
		ctx = nextCtx
		if prepared.Status != operationresult.Success {
			_, err := c.deps.Output.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: prepared.Message})
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
		if directives.WeaveDescription != "" {
			text = strings.TrimSpace(text + "\n\n" + directives.WeaveDescription)
		}
		ctx = agentshare.WithInboundSegments(ctx, replaceInboundTextSegments(ctx, text))
		input := agentshare.InboundTurnInput(ctx, text)
		input.NeedsElyphRules = input.NeedsElyphRules || directives.NeedsElyphRules || skillDirectives.NeedsElyphRules
		ctx = agentshare.WithInboundTurnInput(ctx, input)
		if strings.TrimSpace(text) == "" && !hasInboundNonTextSegment(ctx) {
			if len(directives.Injected) > 0 || len(directives.Existing) > 0 || len(skillDirectives.Skills) > 0 {
				binding, _ := sessionpkg.BindingFromContext(ctx)
				return ctx, c.deps.Sessions.RecordActivity(ctx, binding)
			}
			return ctx, nil
		}
	}

	ctx, disposition, err := c.deps.Execution.AcceptInput(ctx, session, text)
	if err != nil {
		return ctx, err
	}
	if disposition.Status != operationresult.Success {
		_, err := c.deps.Output.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: disposition.Message})
		return ctx, err
	}
	if disposition.Data == agentshare.InputRiskConfirmation {
		current, err := c.deps.Confirmations.CurrentConfirmation(ctx, control.CurrentConfirmationRequest{})
		if err != nil {
			return ctx, err
		}
		if current.Status != operationresult.Success {
			_, err := c.deps.Output.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: current.Message})
			return ctx, err
		}
		text := "无待确认"
		if current.Data.Kind == "tool" {
			text = "待确认\n" + c.deps.CommandText("detail") + ": 详情 · " + c.deps.CommandText("confirm") + ": 确认 · " + c.deps.CommandText("reject") + ": 拒绝 · " + c.deps.CommandText("stop") + ": 停止"
		}
		_, err = c.deps.Output.Send(ctx, delivery.StreamEvent{Type: delivery.StreamSend, Text: text})
		return ctx, err
	}
	return ctx, nil
}

// NewInputCoordinator initializes InputCoordinator; platform-specific normalization is optional.
func hasInboundNonTextSegment(ctx context.Context) bool {
	for _, segment := range agentshare.InboundSegments(ctx, "") {
		if segment.Type != llm.SegmentText {
			return true
		}
	}
	return false
}

func NewInputCoordinator(deps InputCoordinatorDeps) *InputCoordinator {
	return &InputCoordinator{deps: deps}
}
