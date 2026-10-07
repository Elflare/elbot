package agent

import (
	"context"
	"fmt"
	"log/slog"

	"elbot/internal/command"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	globalevents "elbot/internal/events"
	"elbot/internal/session"
	"elbot/internal/turn"
)

type commandExecutor struct {
	router        *command.Router
	sessions      *session.Service
	turns         *turn.Manager
	identity      *identityResolver
	execution     *executionCoordinator
	output        *outputSender
	confirmations *confirmationCoordinator
	input         *inputCoordinator
}

func (e *commandExecutor) Handle(ctx context.Context, text string) (bool, error) {
	if e == nil || e.router == nil || !e.router.IsCommand(text) {
		return false, nil
	}

	parsed := e.router.Parse(text)
	info, hasInfo := e.router.CommandInfo(parsed.Name)
	sessionRow, binding, sessionErr := e.sessions.CurrentBound(ctx, e.identity.Scope(ctx))
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
		e.output.SendChat(ctx, activeTurnCommandBlockedText())
		return true, nil
	}
	if sessionErr == nil && hasInfo && e.execution.compactActive(sessionRow.ID) && blocksDuringCompact(info.SessionEffect) {
		e.output.SendChat(ctx, compactCommandBlockedText(text))
		return true, nil
	}

	actor, _ := contextinfo.ActorFromContext(ctx)
	if hasInfo && !command.CanAccess(info, actor) {
		_ = globalevents.EmitLog(ctx, globalevents.LogRecord{
			Category: globalevents.LogAudit,
			Level:    slog.LevelWarn,
			Name:     "permission_denied",
			Module:   "agent",
			Summary:  "permission_denied",
			Fields:   []slog.Attr{slog.Any("actor_id", actor.ID), slog.Any("command", text), slog.Any("reason", "slash_command_requires_superadmin")},
		})
		e.output.SendChat(ctx, fmt.Sprintf("命令 %s%s 需要超级管理员权限。", parsed.Prefix, parsed.Name))
		return true, nil
	}

	if parsed.Name == "tools" {
		prepared, err := e.execution.modelContext(ctx, sessionRow)
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
		if err := e.output.SendNotice(ctx, delivery.Notice{Outputs: []delivery.Output{delivery.Text(result.Content)}}); err != nil {
			return true, err
		}
	}
	if result.Continuation != nil {
		return true, e.input.continueCommandInput(ctx, *result.Continuation)
	}
	return true, nil
}

func blocksDuringActiveTurn(effect command.SessionEffect) bool {
	return effect&command.SessionEffectSwitchCurrent != 0
}

func blocksDuringCompact(effect command.SessionEffect) bool {
	return effect&(command.SessionEffectSwitchCurrent|command.SessionEffectMutate) != 0
}
