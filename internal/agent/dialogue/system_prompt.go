package dialogue

import (
	agentshare "elbot/internal/agent/share"

	"context"
	"strings"
)

type SystemPromptPart struct {
	Name    string
	Content string
}

type SystemPromptSource interface {
	Parts(ctx context.Context, req agentshare.SystemPromptRequest) ([]SystemPromptPart, error)
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
	m.sources = append(m.sources, source)
}

func (m SystemPromptManager) Build(ctx context.Context, req agentshare.SystemPromptRequest) (string, error) {
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
