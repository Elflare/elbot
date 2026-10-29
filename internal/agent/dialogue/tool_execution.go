package dialogue

import (
	agentshare "elbot/internal/agent/share"

	"context"
	"elbot/internal/tool"
	"fmt"
	"log/slog"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
	"elbot/internal/llm"
	sessionpkg "elbot/internal/session"
	"elbot/internal/storage"

	"elbot/internal/turn"
)

type ToolExecutor struct {
	Messages *MessageStore
	Manager  interface {
		ToolInfo(context.Context, string) (tool.Info, bool, error)
		FilterSchemas(context.Context, []llm.ToolSchema) ([]llm.ToolSchema, error)
		Run(context.Context, tool.RunnerDeps, tool.RunRequest) tool.RunResult
		Schemas(context.Context, tool.Context, []tool.DiscoveredTool) ([]llm.ToolSchema, error)
		DiscoveredToolsForSession(context.Context, string, bool) ([]tool.DiscoveredTool, events.LogResult, error)
		DecodeState(string) (tool.State, error)
	}
	Identity interface {
		Actor(context.Context) contextinfo.Actor
	}
	Deps      agentshare.ToolDepsFactory
	MaxRounds int
}

func (r *ToolExecutor) MaxRoundsPerTurn() int { return r.MaxRounds }
func (r *ToolExecutor) Execute(ctx context.Context, session *storage.Session, calls []llm.ToolCallRequest, assistantText, assistantRawText string, out agentshare.Output) tool.RunResult {
	return r.ExecuteWithCommitter(ctx, session, calls, assistantText, assistantRawText, out, &ToolTranscriptCommitter{Messages: r.Messages})
}

func (r *ToolExecutor) ExecuteWithCommitter(ctx context.Context, session *storage.Session, calls []llm.ToolCallRequest, assistantText, assistantRawText string, out agentshare.Output, committer tool.ToolCommitter) tool.RunResult {
	if session == nil || (session.Mode != storage.SessionModeWork && session.Mode != storage.SessionModeBackground) {
		return tool.RunResult{}
	}
	discovered, logResult, err := r.Manager.DiscoveredToolsForSession(ctx, session.ID, sessionpkg.IsBackground(session))
	if err != nil {
		if logResult.ResultStatus == events.ResultSkipped || ctx.Err() != nil {
			return tool.RunResult{Err: err}
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
		head := agentshare.ToolCallStorageMessage(session.ID, assistantText, assistantRawText, calls)
		{
			logResult, err := committer.Begin(ctx, &head)
			record.Content += "\npersistence:\n" + logResult.Content
			if logResult.ResultStatus == events.ResultFailed {
				record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
			} else if logResult.ResultStatus == events.ResultCanceled && record.ResultStatus != events.ResultFailed {
				record.ResultStatus, record.Level = events.ResultCanceled, slog.LevelInfo
			}
			if err != nil {
				return tool.RunResult{Err: err}
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
			stored := agentshare.ToolResultStorageMessage(session.ID, messages[i])
			logResult, err := committer.Result(ctx, i, calls[i], messages[i], &storage.ToolResultCommit{Result: &stored})
			record.Content += "\npersistence:\n" + logResult.Content
			if logResult.ResultStatus == events.ResultFailed {
				record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
			} else if logResult.ResultStatus == events.ResultCanceled && record.ResultStatus != events.ResultFailed {
				record.ResultStatus, record.Level = events.ResultCanceled, slog.LevelInfo
			}
			if err != nil {
				return tool.RunResult{Err: err}
			}
		}
		_, err = committer.Flush(ctx)
		return tool.RunResult{Messages: messages, PreparedCalls: calls, Err: err}
	}
	return r.Manager.Run(ctx, r.Deps.ForTurn(out, turn.AttemptFromContext(ctx)), tool.RunRequest{
		Session:          session,
		Calls:            calls,
		AssistantText:    assistantText,
		AssistantRawText: assistantRawText,
		DiscoveredTools:  discovered,
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
	return r.Manager.Schemas(ctx, tool.Context{Mode: session.Mode, Actor: r.Identity.Actor(ctx), DisableBaseTools: sessionpkg.IsBackground(session)}, discovered)
}
