package dialogue

import (
	"context"

	"elbot/internal/storage"
	"elbot/internal/toolrun"
)

// The tool module owns tag configuration; Agent only places its prompt material.
type ToolTagsSystemPromptSource struct {
	Preloader *toolrun.PreloadService
}

func (s ToolTagsSystemPromptSource) Parts(ctx context.Context, req SystemPromptRequest) ([]SystemPromptPart, error) {
	if req.Session == nil || (req.Session.Mode != storage.SessionModeWork && req.Session.Mode != storage.SessionModeBackground) {
		return nil, nil
	}
	state, err := toolrun.DecodeState(req.Session.Metadata)
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
