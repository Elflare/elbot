package dialogue

import (
	"context"
	"fmt"

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

func (r *ToolExecutor) MaxRoundsPerTurn() int {
	if r.MaxRounds <= 0 {
		return 2
	}
	return r.MaxRounds
}
func (r *ToolExecutor) Execute(ctx context.Context, session *storage.Session, calls []llm.ToolCallRequest, assistantText, assistantRawText string, out Output) toolrun.RunResult {
	return r.ExecuteWithCommitter(ctx, session, calls, assistantText, assistantRawText, out, &ToolTranscriptCommitter{Messages: r.Messages})
}

func (r *ToolExecutor) ExecuteWithCommitter(ctx context.Context, session *storage.Session, calls []llm.ToolCallRequest, assistantText, assistantRawText string, out Output, committer toolrun.ToolCommitter) toolrun.RunResult {
	if session == nil || (session.Mode != storage.SessionModeWork && session.Mode != storage.SessionModeBackground) {
		return toolrun.RunResult{}
	}
	cached, err := CachedToolsForSession(ctx, r.State, r.Registry, session)
	if err != nil {
		messages := make([]llm.LLMMessage, 0, len(calls))
		transcript := []storage.Message{ToolCallStorageMessage(session.ID, assistantText, assistantRawText, calls)}
		for _, call := range calls {
			message := llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID, Segments: llm.TextSegments(fmt.Sprintf("tool call %s failed: load tool state: %v", call.Name, err))}
			messages = append(messages, message)
			transcript = append(transcript, ToolResultStorageMessage(session.ID, message))
		}
		if err := committer.Begin(ctx, &transcript[0]); err != nil {
			return toolrun.RunResult{Err: err}
		}
		for i := range messages {
			if err := committer.Result(ctx, i, calls[i], messages[i], &transcript[i+1]); err != nil {
				return toolrun.RunResult{Err: err}
			}
		}
		return toolrun.RunResult{Messages: messages, PreparedCalls: calls, Transcript: transcript}
	}
	return r.Manager.Run(ctx, r.Deps.ForTurn(out, turn.AttemptFromContext(ctx)), toolrun.RunRequest{
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
	cached, err := CachedToolsForSession(ctx, r.State, r.Registry, session)
	if err != nil {
		return nil, err
	}
	return r.Manager.Schemas(ctx, toolrun.Context{Mode: session.Mode, Session: session, Scope: r.Identity.Scope(ctx), Actor: r.Identity.Actor(ctx), DisableBaseTools: sessionpkg.IsBackground(session)}, cached)
}

func CachedToolsForSession(ctx context.Context, service *toolrun.StateService, registry *tool.Registry, row *storage.Session) ([]toolrun.CachedTool, error) {
	if row == nil {
		return nil, nil
	}
	state, err := service.Snapshot(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	return state.CachedTools(registry, sessionpkg.IsBackground(row)), nil
}
