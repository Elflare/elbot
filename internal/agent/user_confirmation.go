package agent

import (
	"context"
	"errors"
	"strings"

	"elbot/internal/command"
	"elbot/internal/llm"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func isUserConfirmationCommandName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "yes", "y", "no", "n":
		return true
	default:
		return false
	}
}

func isUserConfirmationConfirmName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "yes", "y":
		return true
	default:
		return false
	}
}

func (a *Agent) handleUserConfirmationCommand(ctx context.Context, parsed command.Parsed) error {
	session, err := a.sessions.Current(ctx, a.scope(ctx))
	if errors.Is(err, storage.ErrNotFound) {
		a.sendChat(ctx, "当前没有等待确认的操作。\n")
		return nil
	}
	if err != nil {
		return err
	}

	confirm := isUserConfirmationConfirmName(parsed.Name)
	snapshot := a.turns.Snapshot(session.ID)
	switch snapshot.Phase {
	case turn.PhaseAwaitAppendConfirm:
		if confirm {
			merged, ok := a.turns.ConfirmAppend(session.ID)
			if !ok || strings.TrimSpace(merged) == "" {
				a.sendChat(ctx, "当前没有等待确认的追加内容。\n")
				return nil
			}
			ctx = withInboundSegments(ctx, llm.TextSegments(merged))
			return a.startChat(ctx, session, merged)
		}
		a.turns.CancelAppend(session.ID)
		a.sendChat(ctx, "已取消追加，本轮处理已停止。\n")
		return nil
	case turn.PhaseAwaitRiskConfirm:
		return a.handleRiskConfirmationInput(ctx, session.ID, riskConfirmationCommandText(parsed, confirm))
	default:
		a.sendChat(ctx, "当前没有等待确认的操作。\n")
		return nil
	}
}

func riskConfirmationCommandText(parsed command.Parsed, confirm bool) string {
	name := parsed.Name
	if confirm {
		name = "confirm"
	} else {
		name = "reject"
	}
	if strings.TrimSpace(parsed.Args) == "" {
		return parsed.Prefix + name
	}
	return parsed.Prefix + name + " " + parsed.Args
}
