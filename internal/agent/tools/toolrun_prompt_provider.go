package tools

import (
	"context"

	"elbot/internal/agent/dialogue"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/contextinfo"
	"elbot/internal/session"
	"elbot/internal/storage"
	toolshare "elbot/internal/tool/share"
)

type toolRunPromptProvider struct {
	tools interface {
		QueryTools(context.Context, toolshare.ToolQuery) ([]toolshare.Info, error)
	}
	identity dialogue.Identity
}

func (p toolRunPromptProvider) ToolNames(ctx context.Context, mode string, session *storage.Session, scope session.Scope) (dialogue.PromptToolNames, error) {
	if err := ctx.Err(); err != nil {
		return dialogue.PromptToolNames{}, err
	}
	if mode != storage.SessionModeWork || agentshare.IsBackgroundSession(session) {
		return dialogue.PromptToolNames{}, nil
	}
	ctx = contextinfo.WithActor(ctx, p.identity.Actor(ctx))
	tools, err := p.tools.QueryTools(ctx, toolshare.ToolQuery{Kind: "tool"})
	if err != nil {
		return dialogue.PromptToolNames{}, err
	}
	skills, err := p.tools.QueryTools(ctx, toolshare.ToolQuery{Kind: "skill"})
	if err != nil {
		return dialogue.PromptToolNames{}, err
	}
	names := dialogue.PromptToolNames{}
	for _, info := range tools {
		names.Tools = append(names.Tools, info.Name)
	}
	for _, info := range skills {
		names.Skills = append(names.Skills, info.Name)
	}
	return names, nil
}
