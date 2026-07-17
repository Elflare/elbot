package agent

import (
	"context"
	"fmt"

	"elbot/internal/command"
	"elbot/internal/security"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

type commandExecutor struct {
	router        *command.Router
	sessions      *session.Service
	turns         *turn.Manager
	scope         func(context.Context) session.Scope
	compactActive func(string) bool
	sendChat      func(context.Context, string)
	sendNotice    func(context.Context, string) error
	audit         func(string, ...any)
	handleAppend  func(context.Context, *storage.Session, string) error
	handleRisk    func(context.Context, string, string) error
	continueInput func(context.Context, command.Continuation) error
}

func (e *commandExecutor) Handle(ctx context.Context, text string) (bool, error) {
	if e == nil || e.router == nil || !e.router.IsCommand(text) {
		return false, nil
	}

	parsed := e.router.Parse(text)
	info, hasInfo := e.router.CommandInfo(parsed.Name)
	sessionRow, sessionErr := e.sessions.Current(ctx, e.scope(ctx))
	if sessionErr == nil && hasInfo && e.compactActive(sessionRow.ID) && blocksDuringCompact(info.SessionEffect) {
		e.sendChat(ctx, compactCommandBlockedText(text))
		return true, nil
	}
	if sessionErr == nil && e.turns.Snapshot(sessionRow.ID).Phase == turn.PhaseAwaitAppendConfirm {
		if turn.IsConfirm(text) || turn.IsCancel(text) {
			return true, e.handleAppend(ctx, sessionRow, text)
		}
		if hasInfo && blocksDuringAppendConfirmation(info.SessionEffect) {
			e.sendChat(ctx, appendConfirmationCommandBlockedText(text))
			return true, nil
		}
	}
	if sessionErr == nil && e.turns.Snapshot(sessionRow.ID).Phase == turn.PhaseAwaitRiskConfirm && isRiskConfirmationCommand(text, e.router) {
		return true, e.handleRisk(ctx, sessionRow.ID, text)
	}

	actor, _ := security.ActorFromContext(ctx)
	if hasInfo && info.MinRole != security.RoleUser && actor.Role != security.RoleSuperadmin {
		e.audit("permission_denied", "actor_id", actor.ID, "command", text, "reason", "slash_command_requires_superadmin")
		e.sendChat(ctx, fmt.Sprintf("命令 %s%s 需要超级管理员权限。", parsed.Prefix, parsed.Name))
		return true, nil
	}

	result, err := e.router.Dispatch(ctx, text)
	if err != nil {
		return true, err
	}
	if result == nil {
		return true, nil
	}
	if result.Content != "" {
		if err := e.sendNotice(ctx, result.Content); err != nil {
			return true, err
		}
	}
	if result.Continuation != nil {
		return true, e.continueInput(ctx, *result.Continuation)
	}
	return true, nil
}

func blocksDuringAppendConfirmation(effect command.SessionEffect) bool {
	return effect&command.SessionEffectSwitchCurrent != 0
}

func blocksDuringCompact(effect command.SessionEffect) bool {
	return effect&(command.SessionEffectSwitchCurrent|command.SessionEffectMutate) != 0
}
