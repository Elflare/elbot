package input

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/command"
	"elbot/internal/control"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/session"
	"elbot/internal/storage"
	"errors"
	"log/slog"
)

type CommandExecutor struct {
	router interface {
		IsCommand(string) bool
		Parse(string) command.Parsed
		CommandInfo(string) (command.Info, bool)
		Dispatch(context.Context, string) (*command.Result, error)
	}
	sessions interface {
		CurrentBound(context.Context, session.Scope) (*storage.Session, *session.Binding, error)
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
	failure := command.ClassifyError(err)
	if failure != nil {
		e.logFailure(ctx, "command_failed", failure)
	}
	sendCtx := session.WithBinding(context.WithoutCancel(origin), nil)
	if _, err := e.output.SendCommandResult(sendCtx, req, result, failure); err != nil {
		e.logFailure(sendCtx, "command_send_failed", err)
		return ctx, true, err
	}
	if failure != nil {
		return ctx, true, failure
	}
	if result != nil && result.Continuation != nil {
		next, err := e.input.ContinueCommandInput(ctx, *result.Continuation)
		if err != nil {
			e.logFailure(next, "command_continuation_failed", err)
			globalevents.Notify(session.WithBinding(context.WithoutCancel(next), nil), globalevents.NewNotification(next, slog.LevelWarn, err.Error()))
			if result.Continuation.Kind == "append" {
				return next, true, nil
			}
		}
		return next, true, err
	}
	return ctx, true, nil
}
func (e *CommandExecutor) execute(ctx context.Context, text string, isCommand bool) (context.Context, *command.Result, bool, error) {
	_, binding, err := e.sessions.CurrentBound(ctx, e.identity.Scope(ctx))
	if err != nil && !errors.Is(err, session.ErrNoCurrentSession) {
		return ctx, nil, true, err
	}
	if err == nil {
		ctx = session.WithBinding(ctx, binding)
	}
	if !isCommand {
		current, err := e.confirmations.CurrentConfirmation(ctx, control.CurrentConfirmationRequest{})
		if err != nil || current.Kind != "append" {
			return ctx, nil, false, nil
		}
		data, err := e.confirmations.RespondConfirmation(ctx, control.RespondConfirmationRequest{Decision: "approve", Scope: "current"})
		if err != nil {
			return ctx, nil, true, err
		}
		if !data.Accepted {
			return ctx, nil, false, nil
		}
		return ctx, &command.Result{Kind: command.ResultKindText, Data: command.TextData{Content: "追加输入已接受"}, Continuation: data.Continuation}, true, err
	}
	result, err := e.router.Dispatch(ctx, text)
	return ctx, result, true, err
}
func (e *CommandExecutor) logFailure(ctx context.Context, event string, err error) {
	level := slog.LevelWarn
	if errors.Is(err, context.Canceled) {
		level = slog.LevelInfo
	}
	content := err.Error()
	var failure *command.Error
	if errors.As(err, &failure) && failure.Cause != nil {
		content += ": " + failure.Cause.Error()
	}
	record := globalevents.LogRecord{Category: globalevents.LogCategoryLog, Level: level, EventName: event, Module: "command", Content: content, ContentLimit: 4096, ResultStatus: globalevents.LogStatus(err)}
	record.Identity = globalevents.LogIdentityFromContext(ctx)
	if failure != nil && (failure.Kind == command.ErrorInput || failure.Kind == command.ErrorRejected) {
		record.ResultStatus = globalevents.ResultRejected
	}
	globalevents.Log(context.WithoutCancel(ctx), record)
}

type CommandOutput interface {
	SendCommandResult(context.Context, command.Request, *command.Result, *command.Error) (delivery.Receipt, error)
}
type CommandConfirmations interface {
	CurrentConfirmation(context.Context, control.CurrentConfirmationRequest) (control.CurrentConfirmationResult, error)
	RespondConfirmation(context.Context, control.RespondConfirmationRequest) (control.RespondConfirmationResult, error)
}
type CommandInput interface {
	ContinueCommandInput(context.Context, control.Continuation) (context.Context, error)
}
type CommandExecutorDeps struct {
	Router interface {
		IsCommand(string) bool
		Parse(string) command.Parsed
		CommandInfo(string) (command.Info, bool)
		Dispatch(context.Context, string) (*command.Result, error)
	}
	Sessions interface {
		CurrentBound(context.Context, session.Scope) (*storage.Session, *session.Binding, error)
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
