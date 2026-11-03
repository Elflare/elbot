package dialogue

import (
	agentshare "elbot/internal/agent/share"

	"context"
	"elbot/internal/tool"
	"fmt"

	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	sessionpkg "elbot/internal/session"
	"elbot/internal/storage"

	"elbot/internal/turn"
)

type ToolExecutor struct {
	Manager interface {
		QueryTools(context.Context, tool.ToolQuery) ([]tool.ToolInfo, error)
		FilterSchemas(context.Context, []llm.ToolSchema) ([]llm.ToolSchema, error)
		Run(context.Context, tool.RunnerDeps, tool.RunRequest) tool.RunResult
		Schemas(context.Context, tool.Context, []tool.DiscoveredTool) ([]llm.ToolSchema, error)
		DiscoveredToolsForSession(context.Context, string, bool) ([]tool.DiscoveredTool, error)
		DecodeState(string) (tool.State, error)
	}
	Identity interface {
		Actor(context.Context) contextinfo.Actor
	}
	Deps      agentshare.ToolDepsFactory
	MaxRounds int
}

func (r *ToolExecutor) MaxRoundsPerTurn() int { return r.MaxRounds }
func (r *ToolExecutor) ExecuteWithCommitter(ctx context.Context, session *storage.Session, calls []llm.ToolCallRequest, assistantText, assistantRawText string, out agentshare.Output, committer tool.ToolCommitter) tool.RunResult {
	if session == nil || (session.Mode != storage.SessionModeWork && session.Mode != storage.SessionModeBackground) {
		return tool.RunResult{}
	}
	discovered, err := r.Manager.DiscoveredToolsForSession(ctx, session.ID, sessionpkg.IsBackground(session))
	if err != nil {
		return tool.RunResult{Err: err}
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
	discovered, err := r.Manager.DiscoveredToolsForSession(ctx, session.ID, sessionpkg.IsBackground(session))
	if err != nil {
		return nil, fmt.Errorf("准备工具: %w", err)
	}
	return r.Manager.Schemas(ctx, tool.Context{Mode: session.Mode, Actor: r.Identity.Actor(ctx), DisableBaseTools: sessionpkg.IsBackground(session)}, discovered)
}
