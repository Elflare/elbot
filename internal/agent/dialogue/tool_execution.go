package dialogue

import (
	"context"
	"fmt"
	"log/slog"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	sessionpkg "elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
	"elbot/internal/turn"
)

type ToolDepsFactory interface {
	ForTurn(Output, string) toolrun.RunnerDeps
}
type ToolExecutor struct {
	Messages        *MessageStore
	Manager         *toolrun.Manager
	State           *toolrun.StateService
	Registry        *tool.Registry
	Provider        ToolSchemaProvider
	DefaultProvider bool
	Identity        Identity
	Deps            ToolDepsFactory
	MaxRounds       int
}

func (r *ToolExecutor) MaxRoundsPerTurn() int { return r.MaxRounds }
func (r *ToolExecutor) Execute(ctx context.Context, session *storage.Session, calls []llm.ToolCallRequest, assistantText, assistantRawText string, out Output) toolrun.RunResult {
	return r.ExecuteWithCommitter(ctx, session, calls, assistantText, assistantRawText, out, &ToolTranscriptCommitter{Messages: r.Messages})
}

func (r *ToolExecutor) ExecuteWithCommitter(ctx context.Context, session *storage.Session, calls []llm.ToolCallRequest, assistantText, assistantRawText string, out Output, committer toolrun.ToolCommitter) toolrun.RunResult {
	if session == nil || (session.Mode != storage.SessionModeWork && session.Mode != storage.SessionModeBackground) {
		return toolrun.RunResult{}
	}
	cached, logResult, err := CachedToolsForSession(ctx, r.State, r.Registry, session)
	if err != nil {
		if logResult.ResultStatus == events.ResultSkipped || ctx.Err() != nil {
			return toolrun.RunResult{Err: err}
		}
		record := events.LogRecord{
			Category:     events.LogRuntime,
			Level:        slog.LevelError,
			EventName:    "tool_batch_prepare_failed",
			Module:       "agent",
			Type:         events.ContentText,
			Content:      logResult.Content,
			ContentLimit: 4096,
			ResultStatus: logResult.ResultStatus,
		}
		record.Identity.SessionID = &session.ID
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
		if logResult.ResultStatus == events.ResultCanceled {
			record.Level = slog.LevelInfo
		}
		defer func() { _ = events.EmitLog(context.WithoutCancel(ctx), record) }()
		messages := make([]llm.LLMMessage, 0, len(calls))
		head := ToolCallStorageMessage(session.ID, assistantText, assistantRawText, calls)
		{
			logResult, err := committer.Begin(ctx, &head)
			record.Content += "\npersistence:\n" + logResult.Content
			if logResult.ResultStatus == events.ResultFailed {
				record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
			} else if logResult.ResultStatus == events.ResultCanceled && record.ResultStatus != events.ResultFailed {
				record.ResultStatus, record.Level = events.ResultCanceled, slog.LevelInfo
			}
			if err != nil {
				return toolrun.RunResult{Err: err}
			}
		}
		for _, call := range calls {
			message := llm.LLMMessage{
				Role:       llm.RoleTool,
				Name:       call.Name,
				ToolCallID: call.ID,
				Segments:   llm.TextSegments(fmt.Sprintf("tool call %s failed: load tool state: %v", call.Name, err)),
			}
			messages = append(messages, message)
		}
		for i := range messages {
			stored := ToolResultStorageMessage(session.ID, messages[i])
			logResult, err := committer.Result(ctx, i, calls[i], messages[i], &stored)
			record.Content += "\npersistence:\n" + logResult.Content
			if logResult.ResultStatus == events.ResultFailed {
				record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
			} else if logResult.ResultStatus == events.ResultCanceled && record.ResultStatus != events.ResultFailed {
				record.ResultStatus, record.Level = events.ResultCanceled, slog.LevelInfo
			}
			if err != nil {
				return toolrun.RunResult{Err: err}
			}
		}
		_, err = committer.Flush(ctx)
		return toolrun.RunResult{Messages: messages, PreparedCalls: calls, Err: err}
	}
	return toolrun.NewRunner(r.Manager).Run(ctx, r.Deps.ForTurn(out, turn.AttemptFromContext(ctx)), toolrun.RunRequest{
		Session:          session,
		Calls:            calls,
		AssistantText:    assistantText,
		AssistantRawText: assistantRawText,
		CachedTools:      cached,
		Actor:            r.Identity.Actor(ctx),
		Committer:        committer,
	})
}

func (r *ToolExecutor) Schemas(ctx context.Context, session *storage.Session) ([]llm.ToolSchema, error) {
	if session == nil || (session.Mode != storage.SessionModeWork && session.Mode != storage.SessionModeBackground) {
		return nil, nil
	}
	if session.Mode == storage.SessionModeWork && r.Provider != nil && !r.DefaultProvider {
		return r.Provider.Schemas(ctx, session.Mode, session, r.Identity.Scope(ctx))
	}
	cached, logResult, err := CachedToolsForSession(ctx, r.State, r.Registry, session)
	if err != nil {
		if logResult.ResultStatus == events.ResultFailed {
			_ = events.EmitLog(context.WithoutCancel(ctx), events.LogRecord{Category: events.LogRuntime, Level: slog.LevelError, EventName: "tool_batch_prepare_failed", Module: "agent", Content: logResult.Content, ResultStatus: logResult.ResultStatus})
			_, _ = events.NotificationRequested.EmitAsync(sessionpkg.WithBinding(context.WithoutCancel(ctx), nil), events.NewNotification(ctx, slog.LevelError, "工具准备失败："+err.Error()))
		}
		return nil, err
	}
	return r.Manager.Schemas(ctx, toolrun.Context{Mode: session.Mode, Session: session, Scope: r.Identity.Scope(ctx), Actor: r.Identity.Actor(ctx), DisableBaseTools: sessionpkg.IsBackground(session)}, cached)
}

func CachedToolsForSession(ctx context.Context, service *toolrun.StateService, registry *tool.Registry, row *storage.Session) ([]toolrun.CachedTool, events.LogResult, error) {
	if row == nil {
		return nil, events.LogResult{ResultStatus: events.ResultSkipped, Content: "session is absent"}, nil
	}
	loaded, err := service.Load(ctx, row.ID)
	if err != nil {
		logResult := events.LogResult{ResultStatus: events.ResultFailed, Content: fmt.Sprintf("tool_state_error=%v", err)}
		if ctx.Err() != nil {
			logResult.ResultStatus = events.ResultCanceled
		}
		return nil, logResult, err
	}
	state, err := toolrun.DecodeState(loaded.Metadata)
	if err != nil {
		return nil, events.LogResult{ResultStatus: events.ResultSkipped}, err
	}
	return state.CachedTools(registry, sessionpkg.IsBackground(row)), events.LogResult{ResultStatus: events.ResultSucceeded, Content: "tool state loaded"}, nil
}
