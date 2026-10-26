package dialogue

import (
	agentshare "elbot/internal/agent/share"

	"context"
	"elbot/internal/tool"

	"elbot/internal/storage"
)

type ToolTagsSystemPromptSource struct {
	Preloader interface {
		TagPrompts(context.Context, []string) ([]tool.TagPrompt, error)
		DecodeState(string) (tool.State, error)
	}
}

func (s ToolTagsSystemPromptSource) Parts(ctx context.Context, req agentshare.SystemPromptRequest) ([]SystemPromptPart, error) {
	if req.Session == nil || (req.Session.Mode != storage.SessionModeWork && req.Session.Mode != storage.SessionModeBackground) {
		return nil, nil
	}
	state, err := s.Preloader.DecodeState(req.Session.Metadata)
	if err != nil {
		return nil, err
	}
	prompts, err := s.Preloader.TagPrompts(ctx, state.ToolTags)
	if err != nil {
		return nil, err
	}
	parts := make([]SystemPromptPart, 0, len(prompts))
	for _, prompt := range prompts {
		parts = append(parts, SystemPromptPart{Name: "tool_tag_prompt:" + prompt.Tag, Content: prompt.Prompt})
	}
	return parts, nil
}
