package agent

import (
	"context"
	"strings"

	"elbot/internal/session"
	"elbot/internal/storage"
)

type SystemPromptPart struct {
	Name    string
	Content string
}

type SystemPromptRequest struct {
	Mode             string
	Session          *storage.Session
	Scope            session.Scope
	ActorDisplayName string
}

type SystemPromptSource interface {
	Parts(ctx context.Context, req SystemPromptRequest) ([]SystemPromptPart, error)
}

type SystemPromptManager struct {
	sources []SystemPromptSource
}

func NewSystemPromptManager(sources ...SystemPromptSource) SystemPromptManager {
	manager := SystemPromptManager{}
	for _, source := range sources {
		manager.AddSource(source)
	}
	return manager
}

func (m *SystemPromptManager) AddSource(source SystemPromptSource) {
	if source == nil {
		return
	}
	m.sources = append(m.sources, source)
}

func (m SystemPromptManager) Build(ctx context.Context, req SystemPromptRequest) (string, error) {
	content := []string{}
	for _, source := range m.sources {
		sourceParts, err := source.Parts(ctx, req)
		if err != nil {
			return "", err
		}
		for _, part := range sourceParts {
			part.Content = strings.TrimSpace(part.Content)
			if part.Content == "" {
				continue
			}
			content = append(content, part.Content)
		}
	}
	return strings.Join(content, "\n\n"), nil
}
