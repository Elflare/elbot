package tools

import (
	"context"
	"elbot/internal/tool"

	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/session"
	"elbot/internal/storage"
)

type toolRunPromptProvider struct {
	tools interface {
		QueryTools(context.Context, tool.ToolQuery) ([]tool.Info, error)
	}
	identity interface {
		Actor(context.Context) contextinfo.Actor
	}
}

func (p toolRunPromptProvider) ToolNames(ctx context.Context, mode string, session *storage.Session, scope session.Scope) (agentshare.PromptToolNames, error) {
	if err := ctx.Err(); err != nil {
		return agentshare.PromptToolNames{}, err
	}
	if mode != storage.SessionModeWork || agentshare.IsBackgroundSession(session) {
		return agentshare.PromptToolNames{}, nil
	}
	ctx = contextinfo.WithActor(ctx, p.identity.Actor(ctx))
	tools, err := p.tools.QueryTools(ctx, tool.ToolQuery{Kind: "tool"})
	if err != nil {
		return agentshare.PromptToolNames{}, err
	}
	skills, err := p.tools.QueryTools(ctx, tool.ToolQuery{Kind: "skill"})
	if err != nil {
		return agentshare.PromptToolNames{}, err
	}
	names := agentshare.PromptToolNames{}
	for _, info := range tools {
		names.Tools = append(names.Tools, info.Name)
	}
	for _, info := range skills {
		names.Skills = append(names.Skills, info.Name)
	}
	return names, nil
}
