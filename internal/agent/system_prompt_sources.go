package agent

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"elbot/internal/memory/resident"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

type conversationMetaSystemPromptSource struct{}

func (conversationMetaSystemPromptSource) Parts(_ context.Context, req SystemPromptRequest) ([]SystemPromptPart, error) {
	meta := req.Meta
	fields := make([]string, 0, 4)
	for _, field := range []struct {
		name  string
		value string
		quote bool
	}{
		{name: "platform", value: meta.Platform},
		{name: "conversation", value: meta.Kind},
		{name: "id", value: meta.ID},
		{name: "display_name", value: meta.DisplayName, quote: true},
	} {
		value := strings.TrimSpace(field.value)
		if value == "" {
			continue
		}
		if field.quote {
			value = strconv.Quote(strings.Join(strings.Fields(value), " "))
		}
		fields = append(fields, field.name+"="+value)
	}
	if len(fields) == 0 {
		return nil, nil
	}
	return []SystemPromptPart{{Name: "conversation_meta", Content: "meta: " + strings.Join(fields, ", ") + "."}}, nil
}

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
	if errors.Is(err, resident.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	content := strings.TrimSpace(memory.Text())
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
	content := toolNamesText(names)
	if content == "" {
		return nil, nil
	}
	return []SystemPromptPart{{Name: "tool_names", Content: content}}, nil
}
