package agent

import (
	"context"

	"elbot/internal/storage"
	"elbot/internal/tool"
)

const (
	systemPromptPrioritySoul      = 0
	systemPromptPriorityToolNames = 100
)

type soulSystemPromptSource struct {
	Soul SoulProvider
}

func (s soulSystemPromptSource) Parts(ctx context.Context, req SystemPromptRequest) ([]SystemPromptPart, error) {
	if s.Soul == nil {
		return nil, nil
	}
	mode := req.Mode
	if mode == "" {
		mode = storage.SessionModeWork
	}
	content, err := s.Soul.SystemPrompt(ctx, mode)
	if err != nil {
		return nil, err
	}
	return []SystemPromptPart{{Name: "soul", Priority: systemPromptPrioritySoul, Content: content}}, nil
}

type toolNamesSystemPromptSource struct {
	Tools ToolNameProvider
}

func (s toolNamesSystemPromptSource) Parts(ctx context.Context, req SystemPromptRequest) ([]SystemPromptPart, error) {
	if s.Tools == nil || req.Session == nil {
		return nil, nil
	}
	if sandbox, ok := tool.SandboxContextFromContext(ctx); ok && sandbox.Background {
		return nil, nil
	}
	names, err := s.Tools.ToolNames(ctx, req.Mode, req.Session, req.Scope)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, nil
	}
	return []SystemPromptPart{{Name: "tool_names", Priority: systemPromptPriorityToolNames, Content: toolNamesText(names)}}, nil
}
