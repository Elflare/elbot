package input

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/command"
	"elbot/internal/control"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/operationresult"
	"elbot/internal/session"
	"log/slog"
)

type CommandExecutor struct {
	router interface {
		IsCommand(string) bool
		Parse(string) command.Parsed
		CommandInfo(string) (command.Info, bool)
		Dispatch(context.Context, string) (command.Result, error)
	}
	sessions interface {
		CurrentBound(context.Context, session.Scope) (operationresult.Result[session.CurrentBoundData], error)
	}
	identity interface {
		Scope(context.Context) session.Scope
	}
	output        CommandOutput
	confirmations CommandConfirmations
	input         CommandInput
}

func (e *CommandExecutor) Handle(ctx context.Context, text string) (context.Context, bool, error) {
	if !e.router.IsCommand(text) && !agentshare.IsAppendConfirm(text) {
		return ctx, false, nil
	}
	origin := ctx
	parsed := e.router.Parse(text)
	req := command.Request{Raw: text, Prefix: parsed.Prefix, Name: parsed.Name, Args: parsed.Args}
	if info, ok := e.router.CommandInfo(parsed.Name); ok {
		req.Name = info.Name
	}
	ctx, result, handled, err := e.execute(ctx, text, parsed.OK)
	if !handled {
		return ctx, false, err
	}
	if !parsed.OK {
		req.Name = "append"
	}
	if err != nil || result.Status != operationresult.Success {
		e.logFailure(ctx, "command_failed", result, err)
	}
	sendCtx := session.WithBinding(context.WithoutCancel(origin), nil)
	if _, sendErr := e.output.SendCommandResult(sendCtx, req, result, err); sendErr != nil {
		e.logFailure(sendCtx, "command_send_failed", command.Result{}, sendErr)
		return ctx, true, sendErr
	}
	if err != nil {
		return ctx, true, err
	}
	if result.Status != operationresult.Success {
		return ctx, true, nil
	}
	if result.Continuation != nil {
		next, continued, err := e.input.ContinueCommandInput(ctx, *result.Continuation)
		if err != nil {
			e.logFailure(next, "command_continuation_failed", command.Result{}, err)
			globalevents.Notify(session.WithBinding(context.WithoutCancel(next), nil), globalevents.NewNotification(next, slog.LevelWarn, err.Error()))
			if result.Continuation.Kind == "append" {
				return next, true, nil
			}
			return next, true, err
		}
		if continued.Status != operationresult.Success {
			e.logFailure(next, "command_continuation_failed", command.Result{Status: continued.Status, Message: continued.Message}, nil)
			result = command.Result{Status: continued.Status, Message: continued.Message}
			if _, sendErr := e.output.SendCommandResult(session.WithBinding(context.WithoutCancel(origin), nil), req, result, nil); sendErr != nil {
				e.logFailure(session.WithBinding(context.WithoutCancel(origin), nil), "command_send_failed", command.Result{}, sendErr)
				return next, true, sendErr
			}
			return next, true, nil
		}
		return next, true, nil
	}
	return ctx, true, nil
}
func (e *CommandExecutor) execute(ctx context.Context, text string, isCommand bool) (context.Context, command.Result, bool, error) {
	current, err := e.sessions.CurrentBound(ctx, e.identity.Scope(ctx))
	if err != nil {
		return ctx, command.Result{}, true, err
	}
	if current.Status == operationresult.Success {
		ctx = session.WithBinding(ctx, current.Data.Binding)
	} else if current.Status != operationresult.NoCurrentSession {
		return ctx, command.Result{Status: current.Status, Message: current.Message}, true, nil
	}
	if !isCommand {
		current, err := e.confirmations.CurrentConfirmation(ctx, control.CurrentConfirmationRequest{})
		if err != nil {
			return ctx, command.Result{}, true, err
		}
		if current.Status != operationresult.Success {
			return ctx, command.Result{Status: current.Status, Message: current.Message}, true, nil
		}
		if current.Data.Kind != "append" {
			return ctx, command.Result{}, false, nil
		}
		result, err := e.confirmations.RespondConfirmation(ctx, control.RespondConfirmationRequest{Decision: "approve", Scope: "current"})
		if err != nil {
			return ctx, command.Result{}, true, err
		}
		if result.Status != operationresult.Success {
			return ctx, command.Result{Status: result.Status, Message: result.Message}, true, nil
		}
		data := result.Data
		return ctx, command.Result{Status: operationresult.Success, Kind: command.ResultKindText, Data: command.TextData{Content: "追加输入已接受"}, Continuation: data.Continuation}, true, nil
	}
	result, err := e.router.Dispatch(ctx, text)
	return ctx, result, true, err
}
func (e *CommandExecutor) logFailure(ctx context.Context, event string, result command.Result, err error) {
	level, status, content := slog.LevelWarn, globalevents.ResultFailed, result.Message
	if err != nil {
		content = err.Error()
		status = globalevents.LogStatus(err)
	} else {
		switch result.Status {
		case operationresult.Canceled:
			status = globalevents.ResultCanceled
		case operationresult.Failed, operationresult.Timeout:
		default:
			status = globalevents.ResultRejected
		}
	}
	if status == globalevents.ResultCanceled {
		level = slog.LevelInfo
	}
	record := globalevents.LogRecord{Category: globalevents.LogCategoryLog, Level: level, EventName: event, Module: "command", Content: content, ContentLimit: 4096, ResultStatus: status}
	record.Identity = globalevents.LogIdentityFromContext(ctx)
	globalevents.Log(context.WithoutCancel(ctx), record)
}

type CommandOutput interface {
	SendCommandResult(context.Context, command.Request, command.Result, error) (delivery.Receipt, error)
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
		Parse(string) command.Parsed
		CommandInfo(string) (command.Info, bool)
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
	return &CommandExecutor{router: deps.Router, sessions: deps.Sessions, identity: deps.Identity, output: deps.Output, confirmations: deps.Confirmations, input: deps.Input}
}
func (e *CommandExecutor) IsCommand(text string) bool { return e.router.IsCommand(text) }
