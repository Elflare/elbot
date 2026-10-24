package tools

import toolshare "elbot/internal/tool/share"

import (
	"context"

	"elbot/internal/agent/dialogue"
	agentshare "elbot/internal/agent/share"
	"elbot/internal/session"
	"elbot/internal/storage"
	
	
)

type toolRunPromptProvider struct {
	tools interface {
		ToolInfos(context.Context, toolshare.Context) ([]toolshare.Info, error)
	}
	identity dialogue.Identity
}

func (p toolRunPromptProvider) ToolNames(ctx context.Context, mode string, session *storage.Session, scope session.Scope) (dialogue.PromptToolNames, error) {
	infos, err := p.tools.ToolInfos(ctx, toolshare.Context{Mode: mode, Session: session, Scope: scope, Actor: p.identity.Actor(ctx), DisableBaseTools: agentshare.IsBackgroundSession(session)})
	if err != nil {
		return dialogue.PromptToolNames{}, err
	}
	return toolshare.PromptNamesFromInfos(infos), nil
}
