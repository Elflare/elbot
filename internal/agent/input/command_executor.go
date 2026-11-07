package input

import (
	"context"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/delivery"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"elbot/internal/command"
	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

type CommandExecutor struct {
	commandText func(string, ...string) string
	router      interface {
		IsCommand(string) bool
		Parse(string) command.Parsed
		CommandInfo(string) (command.Info, bool)
		Dispatch(context.Context, string) (*command.Result, error)
	}
	sessions interface {
		CurrentBound(context.Context, session.Scope) (*storage.Session, *session.Binding, error)
	}
	turns interface {
		Snapshot(string) turn.Snapshot
	}
	identity interface {
		Scope(context.Context) session.Scope
	}
	execution     CommandExecution
	output        CommandOutput
	confirmations CommandConfirmations
	input         CommandInput
}

func (e *CommandExecutor) Handle(ctx context.Context, text string) (context.Context, bool, error) {
	if !e.router.IsCommand(text) && !agentshare.IsAppendConfirm(text) {
		return ctx, false, nil
	}
	parsed := e.router.Parse(text)
	info, known := e.router.CommandInfo(parsed.Name)
	name := parsed.Name
	riskInfo, riskCommand := e.confirmations.CommandInfo(text)
	if known {
		name = info.Name
	} else if riskCommand {
		name = riskInfo.Name
	} else if !parsed.OK {
		name = "append"
	}
	originCtx := ctx
	riskName := ""
	if riskCommand {
		riskName = riskInfo.Name
	}
	ctx, result, handled, err := e.execute(ctx, text, parsed, info, known, riskName)
	if !handled {
		return ctx, false, err
	}
	failure := command.ClassifyError(err)
	if failure != nil {
		e.logFailure(ctx, "command_failed", failure)
	}
	// 会话命令可能已解除旧绑定；回复仍发给原始输入目标。
	sendCtx := session.WithBinding(context.WithoutCancel(originCtx), nil)
	_, sendErr := e.output.SendCommandResult(sendCtx, name, result, failure)
	if sendErr != nil {
		e.logFailure(sendCtx, "command_send_failed", sendErr)
		return ctx, true, sendErr
	}
	if failure != nil {
		return ctx, true, failure
	}
	if result != nil && result.Continuation != nil {
		next, nextErr := e.input.ContinueCommandInput(ctx, *result.Continuation)
		if nextErr != nil {
			e.logFailure(next, "command_continuation_failed", nextErr)
			globalevents.Notify(session.WithBinding(context.WithoutCancel(next), nil), globalevents.NewNotification(next, slog.LevelWarn, nextErr.Error()))
			if result.Continuation.Kind == command.ContinuationAppend {
				return next, true, nil
			}
		}
		return next, true, nextErr
	}
	return ctx, true, nil
}

func (e *CommandExecutor) execute(ctx context.Context, text string, parsed command.Parsed, info command.Info, known bool, riskName string) (context.Context, *command.Result, bool, error) {
	actor, _ := contextinfo.ActorFromContext(ctx)
	if known && !command.CanAccess(info, actor) {
		content, _ := json.Marshal(map[string]any{"actor": actor, "command": text, "reason": "slash_command_requires_superadmin"})
		record := globalevents.LogRecord{Category: globalevents.LogAudit, Level: slog.LevelWarn, EventName: "permission_denied", Module: "command", Type: globalevents.ContentJSON, Content: string(content), ContentLimit: 4096, ResultStatus: globalevents.ResultRejected}
		record.Identity = globalevents.LogIdentityFromContext(ctx)
		globalevents.Log(ctx, record)
		return ctx, nil, true, &command.Error{Kind: command.ErrorRejected, Code: command.CodePermissionDenied, Message: fmt.Sprintf("%s%s: superadmin required", parsed.Prefix, parsed.Name)}
	}
	row, binding, err := e.sessions.CurrentBound(ctx, e.identity.Scope(ctx))
	if err != nil && !errors.Is(err, session.ErrNoCurrentSession) {
		return ctx, nil, true, err
	}
	snapshot := turn.Snapshot{Phase: turn.PhaseIdle}
	if err == nil {
		ctx = session.WithBinding(ctx, binding)
		snapshot = e.turns.Snapshot(row.ID)
	}
	appendResponse := text
	if riskName == "confirm" {
		appendResponse = "y"
	} else if riskName == "reject" {
		appendResponse = "n"
	}
	if row != nil && snapshot.Phase == turn.PhaseAwaitAppendConfirm && agentshare.IsAppendConfirm(appendResponse) {
		return ctx, &command.Result{Kind: command.ResultKindText, Data: command.TextData{Content: "追加输入已接受"}, Continuation: &command.Continuation{Kind: command.ContinuationAppend, SessionID: row.ID}}, true, nil
	}
	if !parsed.OK {
		return ctx, nil, false, nil
	}
	if row != nil && snapshot.Phase == turn.PhaseAwaitRiskConfirm && riskName != "" {
		result, err := e.confirmations.SubmitResponse(ctx, row.ID, text)
		return ctx, result, true, err
	}
	if !known {
		if riskName != "" {
			return ctx, nil, true, &command.Error{Kind: command.ErrorRejected, Code: command.CodeConfirmationExpired, Message: "当前没有待处理的风险确认"}
		}
		result, err := e.router.Dispatch(ctx, text)
		return ctx, result, true, err
	}
	if row != nil && snapshot.Phase != turn.PhaseIdle && blocksDuringActiveTurn(info.SessionEffect) {
		return ctx, nil, true, &command.Error{Kind: command.ErrorRejected, Code: command.CodeSessionBusy, Message: fmt.Sprintf("%v，请等待完成，或先用 %s 停止任务后再切换。", session.ErrSessionBusy, e.commandText("stop")), Cause: session.ErrSessionBusy}
	}
	if row != nil && e.execution.CompactActive(row.ID) && blocksDuringCompact(info.SessionEffect) {
		return ctx, nil, true, &command.Error{Kind: command.ErrorRejected, Code: command.CodeSessionBusy, Message: compactCommandBlockedText(text, e.commandText), Cause: session.ErrSessionBusy}
	}
	if parsed.Name == "tools" {
		prepared, err := e.execution.ModelContext(ctx, row)
		if err != nil {
			return ctx, nil, true, err
		}
		ctx = prepared
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
	record := globalevents.LogRecord{Category: globalevents.LogRuntime, Level: level, EventName: event, Module: "command", Content: content, ContentLimit: 4096, ResultStatus: globalevents.LogStatus(err)}
	record.Identity = globalevents.LogIdentityFromContext(ctx)
	if failure != nil && (failure.Kind == command.ErrorInput || failure.Kind == command.ErrorRejected) {
		record.ResultStatus = globalevents.ResultRejected
	}
	globalevents.Log(context.WithoutCancel(ctx), record)
}

func blocksDuringActiveTurn(effect command.SessionEffect) bool {
	return effect&command.SessionEffectSwitchCurrent != 0
}

func blocksDuringCompact(effect command.SessionEffect) bool {
	return effect&(command.SessionEffectSwitchCurrent|command.SessionEffectMutate) != 0
}

type CommandExecution interface {
	CompactActive(string) bool
	ModelContext(context.Context, *storage.Session) (context.Context, error)
}

type CommandOutput interface {
	SendCommandResult(context.Context, string, *command.Result, *command.Error) (delivery.Receipt, error)
}

type CommandConfirmations interface {
	CommandInfo(string) (command.Info, bool)
	SubmitResponse(context.Context, string, string) (*command.Result, error)
}

type CommandInput interface {
	ContinueCommandInput(context.Context, command.Continuation) (context.Context, error)
}

// CommandExecutorDeps declares only the capabilities consumed by CommandExecutor.
type CommandExecutorDeps struct {
	CommandText func(string, ...string) string // 根部绑定配置首个前缀的命令组装能力。
	Router      interface {
		IsCommand(string) bool
		Parse(string) command.Parsed
		CommandInfo(string) (command.Info, bool)
		Dispatch(context.Context, string) (*command.Result, error)
	}
	Sessions interface {
		CurrentBound(context.Context, session.Scope) (*storage.Session, *session.Binding, error)
	}
	Turns interface {
		Snapshot(string) turn.Snapshot
	}
	Identity interface {
		Scope(context.Context) session.Scope
	}
	Execution     CommandExecution
	Output        CommandOutput
	Confirmations CommandConfirmations
	Input         CommandInput
}

// NewCommandExecutor initializes CommandExecutor without installing cross-service callbacks.
func NewCommandExecutor(deps CommandExecutorDeps) *CommandExecutor {
	return &CommandExecutor{
		commandText:   deps.CommandText,
		router:        deps.Router,
		sessions:      deps.Sessions,
		turns:         deps.Turns,
		identity:      deps.Identity,
		execution:     deps.Execution,
		output:        deps.Output,
		confirmations: deps.Confirmations,
		input:         deps.Input,
	}
}

func (e *CommandExecutor) IsCommand(text string) bool { return e.router.IsCommand(text) }
