package builtin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"elbot/internal/command"
	"elbot/internal/config"
	"elbot/internal/contextinfo"
	globalevents "elbot/internal/events"
	"elbot/internal/modelmgr"
	"elbot/internal/session"
	"elbot/internal/storage"
)

func NewNew(deps Deps) command.Handler {
	return command.NewFunc(command.Info{Name: "new", Args: "", Description: "重置会话", SessionEffect: command.SessionEffectSwitchCurrent, MinRole: contextinfo.RoleUser}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		if err := deps.Sessions.ResetCurrent(ctx, deps.Scope(ctx)); err != nil {
			return nil, err
		}
		return &command.Result{Content: "OK"}, nil
	})
}

func NewStatus(deps Deps) command.Handler {
	return command.NewFunc(command.Info{Name: "status", Args: "", Description: "会话状态", MinRole: contextinfo.RoleUser}, func(ctx context.Context, req command.Request) (*command.Result, error) {
		status, err := deps.Sessions.Status(ctx, deps.Scope(ctx))
		if errors.Is(err, session.ErrNoCurrentSession) {
			return &command.Result{Content: formatEmptyStatus(ctx, deps)}, nil
		}
		if err != nil {
			return nil, err
		}
		s := status.Session
		archived := ""
		if s.ArchivedAt != nil {
			archived = "archived"
		}
		pinned := ""
		if s.PinnedAt != nil {
			pinned = "pinned"
			if archived != "" {
				pinned += ", "
			}
		}
		active := formatActiveRequests(ctx, deps, deps.Requests.ListBySession(s.ID))
		turnSnapshot := deps.Turns.Snapshot(s.ID)
		pendingInput := fmt.Sprintf("%d", turnSnapshot.PendingCount)
		currentModel := deps.Models.CurrentModelForMode(s.Mode)
		return &command.Result{Content: fmt.Sprintf(`%s · %s
%s · %s/%s · msgs=%d
%screated=%s
updated=%s
u: %s
b: %s
%sphase=%s · pending=%s
%s
%s`, s.ID, s.Title, s.Mode, currentModel.Provider, currentModel.Model, status.MessageCount, indentStatusBlock(pinned+archived), formatTime(s.CreatedAt), formatTime(s.UpdatedAt), emptyTODO(sessionStatusPreview(status.LastUserContent)), emptyTODO(sessionStatusPreview(status.LastAnswerContent)), indentStatusBlock(active), turnSnapshot.Phase, pendingInput, formatToolUsage(ctx, deps, s.ID, turnSnapshot.Tools), formatContextStatus(ctx, deps, s, currentModel))}, nil
	})
}

func formatContextStatus(ctx context.Context, deps Deps, row *storage.Session, model modelmgr.ModelOption) string {
	if deps.Contexts == nil {
		return ""
	}
	usage, err := deps.Usage(row)
	if err != nil {
		record := globalevents.LogRecord{
			Category:     globalevents.LogRuntime,
			Level:        slog.LevelError,
			EventName:    "load_usage_failed",
			Module:       "command",
			Content:      "load usage failed: " + err.Error(),
			Identity:     globalevents.LogIdentity{SessionID: &row.ID},
			ResultStatus: globalevents.ResultFailed,
		}
		facts, _ := contextinfo.ExecutionFromContext(ctx)
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
	return deps.Contexts.Status(ctx, usage, config.ModelSelection{Provider: model.Provider, Model: model.Model})
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
	persisted, current := toolsString(usage), toolsString(live)
	if persisted == "" {
		persisted = "nil"
	}
	if current == "" {
		current = "nil"
	}
	return fmt.Sprintf("tools.saved: %s\ntools.turn: %s", persisted, current)
}
