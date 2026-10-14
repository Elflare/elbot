package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"elbot/internal/agent/dialogue"
	"elbot/internal/command"
	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

type CommandExecutor struct {
	router        *command.Router
	sessions      *session.Service
	turns         *turn.Manager
	identity      dialogue.Identity
	execution     CommandExecution
	output        CommandOutput
	confirmations CommandConfirmations
	input         CommandInput
}

func (e *CommandExecutor) Handle(ctx context.Context, text string) (bool, error) {
	if !e.router.IsCommand(text) {
		return false, nil
	}

	parsed := e.router.Parse(text)
	info, hasInfo := e.router.CommandInfo(parsed.Name)
	sessionRow, binding, sessionErr := e.sessions.CurrentBound(ctx, e.identity.Scope(ctx))
	if sessionErr != nil && !errors.Is(sessionErr, storage.ErrNotFound) {
		return true, sessionErr
	}
	if sessionErr == nil {
		ctx = session.WithBinding(ctx, binding)
	}
	snapshot := turn.Snapshot{Phase: turn.PhaseIdle}
	if sessionErr == nil {
		snapshot = e.turns.Snapshot(sessionRow.ID)
	}
	if sessionErr == nil && snapshot.Phase == turn.PhaseAwaitAppendConfirm && (turn.IsConfirm(text) || turn.IsCancel(text)) {
		return true, e.execution.ResumeAppend(ctx, sessionRow, text)
	}
	if sessionErr == nil && snapshot.Phase == turn.PhaseAwaitRiskConfirm && isRiskConfirmationCommand(text, e.router) {
		return true, e.confirmations.SubmitResponse(ctx, sessionRow.ID, text)
	}
	if sessionErr == nil && hasInfo && snapshot.Phase != turn.PhaseIdle && blocksDuringActiveTurn(info.SessionEffect) {
		_, err := globalevents.NotificationRequested.Emit(session.WithBinding(context.WithoutCancel(ctx), nil), globalevents.NewNotification(ctx, slog.LevelWarn, session.ErrSessionBusy.Error()))
		return true, err
	}
	if sessionErr == nil && hasInfo && e.execution.CompactActive(sessionRow.ID) && blocksDuringCompact(info.SessionEffect) {
		return true, e.output.SendChat(ctx, compactCommandBlockedText(text))
	}

	actor, _ := contextinfo.ActorFromContext(ctx)
	if hasInfo && !command.CanAccess(info, actor) {
		{
			content, _ := json.Marshal(map[string]any{"actor_id": actor.ID, "command": text, "reason": "slash_command_requires_superadmin"})
			record := globalevents.LogRecord{
				Category:     globalevents.LogAudit,
				Level:        slog.LevelWarn,
				EventName:    "permission_denied",
				Module:       "agent",
				Type:         globalevents.ContentJSON,
				Content:      string(content),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultRejected,
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
			_ = globalevents.EmitLog(ctx, record)
		}
		return true, e.output.SendChat(ctx, fmt.Sprintf("命令 %s%s 需要超级管理员权限。", parsed.Prefix, parsed.Name))
	}

	if parsed.Name == "tools" {
		prepared, err := e.execution.ModelContext(ctx, sessionRow)
		if err == nil {
			ctx = prepared
		} else {
			ctx = contextinfo.WithoutModel(ctx)
		}
	}
	result, err := e.router.Dispatch(ctx, text)
	if err != nil {
		return true, err
	}
	if result == nil {
		return true, nil
	}
	if result.Content != "" {
		// A command can invalidate its old session binding. Its result still
		// belongs to the original conversation, independently of that activation.
		if _, err := globalevents.NotificationRequested.Emit(session.WithBinding(ctx, nil), globalevents.NewNotification(ctx, slog.LevelInfo, result.Content)); err != nil {
			return true, err
		}
	}
	if result.Continuation != nil {
		return true, e.input.ContinueCommandInput(ctx, *result.Continuation)
	}
	return true, nil
}

func blocksDuringActiveTurn(effect command.SessionEffect) bool {
	return effect&command.SessionEffectSwitchCurrent != 0
}

func blocksDuringCompact(effect command.SessionEffect) bool {
	return effect&(command.SessionEffectSwitchCurrent|command.SessionEffectMutate) != 0
}

type CommandExecution interface {
	ResumeAppend(context.Context, *storage.Session, string) error
	CompactActive(string) bool
	ModelContext(context.Context, *storage.Session) (context.Context, error)
}

type CommandOutput interface {
	SendChat(context.Context, string) error
}

type CommandConfirmations interface {
	SubmitResponse(context.Context, string, string) error
}

type CommandInput interface {
	ContinueCommandInput(context.Context, command.Continuation) error
}

// CommandExecutorDeps declares only the capabilities consumed by CommandExecutor.
type CommandExecutorDeps struct {
	Router        *command.Router
	Sessions      *session.Service
	Turns         *turn.Manager
	Identity      dialogue.Identity
	Execution     CommandExecution
	Output        CommandOutput
	Confirmations CommandConfirmations
	Input         CommandInput
}

// NewCommandExecutor initializes CommandExecutor without installing cross-service callbacks.
func NewCommandExecutor(deps CommandExecutorDeps) *CommandExecutor {
	return &CommandExecutor{
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
