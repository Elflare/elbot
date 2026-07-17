package commands

import (
	"context"
	"errors"
	"fmt"

	"elbot/internal/command"
	"elbot/internal/security"
	sessionpkg "elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/turn"
)

func NewNew(deps Deps) command.Handler {
	return command.NewFunc(command.Info{Name: "new", Usage: "/new", Description: "Create and switch to a new session.", SessionEffect: command.SessionEffectSwitchCurrent, MinRole: security.RoleUser}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		session, err := deps.Sessions.Create(ctx, deps.Scope(ctx), sessionpkg.CreateRequest{Title: "New session"})
		if err != nil {
			return nil, err
		}
		return &command.Result{Content: fmt.Sprintf("created new session:\n  id: %s\n  title: %s\n  mode: %s", session.ID, session.Title, session.Mode)}, nil
	})
}

func NewStatus(deps Deps) command.Handler {
	return command.NewFunc(command.Info{Name: "status", Usage: "/status", Description: "Show current session status.", MinRole: security.RoleUser}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		status, err := deps.Sessions.Status(ctx, deps.Scope(ctx))
		if errors.Is(err, storage.ErrNotFound) {
			return &command.Result{Content: formatEmptyStatus(ctx, deps)}, nil
		}
		if err != nil {
			return nil, err
		}
		s := status.Session
		archived := "no"
		if s.ArchivedAt != nil {
			archived = formatTime(*s.ArchivedAt)
		}
		pinned := "no"
		if s.PinnedAt != nil {
			pinned = formatTime(*s.PinnedAt)
		}
		active := formatActiveRequests(ctx, deps, deps.Requests.ListBySession(s.ID))
		turnSnapshot := deps.Turns.Snapshot(s.ID)
		pendingInput := "none"
		if turnSnapshot.PendingCount > 0 {
			pendingInput = fmt.Sprintf("%d message(s)", turnSnapshot.PendingCount)
		}
		currentModel := deps.Models.CurrentModelForMode(s.Mode)
		return &command.Result{Content: fmt.Sprintf(`session status:
  id: %s
  title: %s
  mode: %s
  current model: %s/%s
  message count: %d
  archived: %s
  pinned: %s
  created at: %s
  updated at: %s
  last ask: %s
  last answer: %s
%s  turn phase: %s
  pending input: %s
  tool usage: %s
%s`, s.ID, s.Title, s.Mode, currentModel.Provider, currentModel.Model, status.MessageCount, archived, pinned, formatTime(s.CreatedAt), formatTime(s.UpdatedAt), emptyTODO(status.LastUserPreview), emptyTODO(status.LastAnswerPreview), indentStatusBlock(active), turnSnapshot.Phase, pendingInput, formatToolUsage(ctx, deps, s.ID, turnSnapshot.Tools), deps.ContextStatus.ContextStatus(ctx, s))}, nil
	})
}

func formatToolUsage(ctx context.Context, deps Deps, sessionID string, live map[string]int) string {
	usage := map[string]int{}
	if deps.Store != nil && deps.Store.ToolCalls() != nil {
		if summaries, err := deps.Store.ToolCalls().UsageBySession(ctx, sessionID); err == nil {
			for _, summary := range summaries {
				if summary.ToolName != "" && summary.Count > 0 {
					usage[summary.ToolName] += summary.Count
				}
			}
		}
	}
	for name, count := range live {
		if count > 0 {
			usage[name] += count
		}
	}
	if out := turn.ToolsString(usage); out != "" {
		return out
	}
	return "none"
}
