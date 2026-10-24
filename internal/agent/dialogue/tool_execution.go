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
	toolrun "elbot/internal/tool/share"
	"elbot/internal/turn"
)

type ToolDepsFactory interface {
	ForTurn(Output, string) toolrun.RunnerDeps
}
type ToolExecutor struct {
	Messages *MessageStore
	Manager  interface {
		Run(context.Context, toolrun.RunnerDeps, toolrun.RunRequest) toolrun.RunResult
		Schemas(context.Context, toolrun.Context, []toolrun.DiscoveredTool) ([]llm.ToolSchema, error)
		DiscoveredToolsForSession(context.Context, string, bool) ([]toolrun.DiscoveredTool, events.LogResult, error)
		DecodeState(string) (toolrun.State, error)
	}
	Identity  Identity
	Deps      ToolDepsFactory
	MaxRounds int
}

func (r *ToolExecutor) MaxRoundsPerTurn() int { return r.MaxRounds }
func (r *ToolExecutor) Execute(ctx context.Context, session *storage.Session, calls []llm.ToolCallRequest, assistantText, assistantRawText string, out Output) toolrun.RunResult {
	return r.ExecuteWithCommitter(ctx, session, calls, assistantText, assistantRawText, out, &ToolTranscriptCommitter{Messages: r.Messages})
}

func (r *ToolExecutor) ExecuteWithCommitter(ctx context.Context, session *storage.Session, calls []llm.ToolCallRequest, assistantText, assistantRawText string, out Output, committer toolrun.ToolCommitter) toolrun.RunResult {
	if session == nil || (session.Mode != storage.SessionModeWork && session.Mode != storage.SessionModeBackground) {
		return toolrun.RunResult{}
	}
	discovered, logResult, err := r.Manager.DiscoveredToolsForSession(ctx, session.ID, sessionpkg.IsBackground(session))
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
			logResult, err := committer.Result(ctx, i, calls[i], messages[i], &storage.ToolResultCommit{Result: &stored})
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
	return r.Manager.Run(ctx, r.Deps.ForTurn(out, turn.AttemptFromContext(ctx)), toolrun.RunRequest{
		Session:          session,
		Calls:            calls,
		AssistantText:    assistantText,
		AssistantRawText: assistantRawText,
		DiscoveredTools:      discovered,
		Actor:            r.Identity.Actor(ctx),
		Committer:        committer,
	})
}

func (r *ToolExecutor) Schemas(ctx context.Context, session *storage.Session) ([]llm.ToolSchema, error) {
	if session == nil || (session.Mode != storage.SessionModeWork && session.Mode != storage.SessionModeBackground) {
		return nil, nil
	}
	discovered, _, err := r.Manager.DiscoveredToolsForSession(ctx, session.ID, sessionpkg.IsBackground(session))
	if err != nil {
		return nil, fmt.Errorf("准备工具: %w", err)
	}
	return r.Manager.Schemas(ctx, toolrun.Context{Mode: session.Mode, Session: session, Scope: r.Identity.Scope(ctx), Actor: r.Identity.Actor(ctx), DisableBaseTools: sessionpkg.IsBackground(session)}, discovered)
}
