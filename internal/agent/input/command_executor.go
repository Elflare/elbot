package input

import (
	"context"
	"elbot/internal/command"
	"elbot/internal/control"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/operationresult"
	"elbot/internal/session"
	"log/slog"
	"strings"
)

type CommandExecutor struct {
	deps CommandExecutorDeps
}

func (e *CommandExecutor) Handle(ctx context.Context, text string) (context.Context, bool, error) {
	isCommand := e.deps.Router.IsCommand(text)
	if !isCommand && !strings.EqualFold(strings.TrimSpace(text), "y") {
		return ctx, false, nil
	}
	origin := ctx
	ctx, result, handled, err := e.execute(ctx, text, isCommand)
	if !handled {
		return ctx, false, err
	}
	sendCtx := session.WithBinding(context.WithoutCancel(origin), nil)
	if _, sendErr := e.deps.Output.SendCommandResult(sendCtx, result, err); sendErr != nil {
		result.Continuation.CancelReservation()
		return ctx, true, sendErr
	}
	if err != nil {
		return ctx, true, err
	}
	if result.Status != operationresult.Success {
		return ctx, true, nil
	}
	if result.Continuation != nil {
		next, continued, err := e.deps.Input.ContinueCommandInput(ctx, *result.Continuation)
		if err != nil {
			globalevents.Notify(session.WithBinding(context.WithoutCancel(next), nil), globalevents.NewNotification(next, slog.LevelWarn, err.Error()))
			if result.Continuation.Kind == "append" {
				return next, true, nil
			}
			return next, true, err
		}
		if continued.Status != operationresult.Success {
			result = command.TextResult(continued.Status, continued.Message)
			if _, sendErr := e.deps.Output.SendCommandResult(sendCtx, result, nil); sendErr != nil {
				return next, true, sendErr
			}
		}
		return next, true, nil
	}
	return ctx, true, nil
}
func (e *CommandExecutor) execute(ctx context.Context, text string, isCommand bool) (context.Context, command.Result, bool, error) {
	current, err := e.deps.Sessions.CurrentBound(ctx, e.deps.Identity.Scope(ctx))
	if err != nil {
		return ctx, command.Result{}, true, err
	}
	if current.Status == operationresult.Success {
		ctx = session.WithBinding(ctx, current.Data.Binding)
	} else if current.Status != operationresult.NoCurrentSession {
		return ctx, command.TextResult(current.Status, current.Message), true, nil
	}
	if !isCommand {
		current, err := e.deps.Confirmations.CurrentConfirmation(ctx, control.CurrentConfirmationRequest{})
		if err != nil {
			return ctx, command.Result{}, true, err
		}
		if current.Status != operationresult.Success {
			return ctx, command.TextResult(current.Status, current.Message), true, nil
		}
		if current.Data.Kind != "append" {
			return ctx, command.Result{}, false, nil
		}
		result, err := e.deps.Confirmations.RespondConfirmation(ctx, control.RespondConfirmationRequest{Decision: "approve", Scope: "current"})
		if err != nil {
			return ctx, command.Result{}, true, err
		}
		if result.Status != operationresult.Success {
			return ctx, command.TextResult(result.Status, result.Message), true, nil
		}
		data := result.Data
		return ctx, command.Result{Status: operationresult.Success, Kind: command.ResultKindText, Blocks: []delivery.ContentBlock{{Kind: delivery.ContentText, Content: "追加输入已接受"}}, Continuation: data.Continuation}, true, nil
	}
	result, err := e.deps.Router.Dispatch(ctx, text)
	return ctx, result, true, err
}

type CommandOutput interface {
	SendCommandResult(context.Context, command.Result, error) (delivery.Receipt, error)
}
type CommandConfirmations interface {
	CurrentConfirmation(context.Context, control.CurrentConfirmationRequest) (operationresult.Result[control.CurrentConfirmationResult], error)
	RespondConfirmation(context.Context, control.RespondConfirmationRequest) (operationresult.Result[control.RespondConfirmationResult], error)
}
type CommandInput interface {
	ContinueCommandInput(context.Context, control.Continuation) (context.Context, operationresult.Result[struct{}], error)
}
type CommandExecutorDeps struct {
	Router interface {
		IsCommand(string) bool
		Dispatch(context.Context, string) (command.Result, error)
	}
	Sessions interface {
		CurrentBound(context.Context, session.Scope) (operationresult.Result[session.CurrentBoundData], error)
	}
	Identity interface {
		Scope(context.Context) session.Scope
	}
	Output        CommandOutput
	Confirmations CommandConfirmations
	Input         CommandInput
}

func NewCommandExecutor(deps CommandExecutorDeps) *CommandExecutor {
	return &CommandExecutor{deps: deps}
}
func (e *CommandExecutor) IsCommand(text string) bool { return e.deps.Router.IsCommand(text) }
