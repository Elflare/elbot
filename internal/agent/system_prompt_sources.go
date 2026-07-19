package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"elbot/internal/memory/resident"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

type soulSystemPromptSource struct {
	Soul SoulProvider
}

type residentMemorySystemPromptSource struct {
	Store *resident.Store
}

func (s residentMemorySystemPromptSource) Parts(ctx context.Context, req SystemPromptRequest) ([]SystemPromptPart, error) {
	if s.Store == nil {
		return nil, nil
	}
	memory, err := s.Store.Read(ctx, req.Scope)
	content := ""
	if errors.Is(err, resident.ErrNotFound) {
		name := strings.TrimSpace(req.ActorDisplayName)
		if name == "" && req.Scope.IsCLI {
			name = "管理员"
		}
		if name != "" {
			content = fmt.Sprintf("用户名字：%s。", name)
		}
	} else if err != nil {
		return nil, err
	} else {
		content = memory.Text()
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, nil
	}
	return []SystemPromptPart{{Name: "resident_memory", Content: content}}, nil
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
	return []SystemPromptPart{{Name: "soul", Content: content}}, nil
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
	return []SystemPromptPart{{Name: "tool_names", Content: toolNamesText(names)}}, nil
}
