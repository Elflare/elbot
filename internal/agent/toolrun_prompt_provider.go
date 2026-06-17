package agent

import (
	"context"

	"elbot/internal/llm"
	"elbot/internal/session"
	"elbot/internal/storage"
	"elbot/internal/toolrun"
)

type toolRunPromptProvider struct {
	agent *Agent
}

func (p toolRunPromptProvider) Schemas(ctx context.Context, mode string, session *storage.Session, scope session.Scope) ([]llm.ToolSchema, error) {
	if p.agent == nil || session == nil {
		return nil, nil
	}
	return p.agent.toolRunManager().BaseSchemas(ctx, toolrun.Context{Mode: mode, Session: session, Scope: scope, Actor: p.agent.actor(ctx)})
}

func (p toolRunPromptProvider) ToolNames(ctx context.Context, mode string, session *storage.Session, scope session.Scope) ([]string, error) {
	if p.agent == nil || session == nil {
		return nil, nil
	}
	return p.agent.toolRunManager().ToolNames(ctx, toolrun.Context{Mode: mode, Session: session, Scope: scope, Actor: p.agent.actor(ctx)})
}
