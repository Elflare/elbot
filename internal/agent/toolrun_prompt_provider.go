package agent

import (
	"context"

	"elbot/internal/agent/dialogue"
	"elbot/internal/llm"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
)

type toolRunPromptProvider struct {
	tools    *toolrun.Manager
	identity *identityResolver
}

func (p toolRunPromptProvider) Schemas(ctx context.Context, mode string, session *storage.Session, scope session.Scope) ([]llm.ToolSchema, error) {
	if p.tools == nil || session == nil {
		return nil, nil
	}
	return p.tools.BaseSchemas(ctx, toolrun.Context{Mode: mode, Session: session, Scope: scope, Actor: p.identity.Actor(ctx), DisableBaseTools: isBackgroundSession(session)})
}

func (p toolRunPromptProvider) ToolNames(ctx context.Context, mode string, session *storage.Session, scope session.Scope) (dialogue.PromptToolNames, error) {
	if p.tools == nil || session == nil {
		return dialogue.PromptToolNames{}, nil
	}
	infos, err := p.tools.ToolInfos(ctx, toolrun.Context{Mode: mode, Session: session, Scope: scope, Actor: p.identity.Actor(ctx), DisableBaseTools: isBackgroundSession(session)})
	if err != nil {
		return dialogue.PromptToolNames{}, err
	}
	return tool.PromptNamesFromInfos(infos), nil
}
