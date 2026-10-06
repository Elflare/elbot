package media

import (
	"context"

	"elbot/internal/llm"
)

// AcquireForLLM owns all holds and temporary request material until release.
func (m *Manager) AcquireForLLM(ctx context.Context, messages []llm.LLMMessage) ([]llm.LLMMessage, func(), error) {
	releases := []func(){}
	release := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	seen := map[string]bool{}
	for _, message := range messages {
		for _, segment := range message.Segments {
			if segment.MediaID == "" || seen[segment.MediaID] {
				continue
			}
			seen[segment.MediaID] = true
			done, err := m.Hold(ctx, segment.MediaID)
			if err != nil {
				release()
				return nil, func() {}, err
			}
			releases = append(releases, func() { _ = done() })
		}
	}
	resolved, cleanup, err := m.ResolveForLLM(ctx, messages)
	if err != nil {
		release()
		return nil, func() {}, err
	}
	releases = append(releases, cleanup)
	return resolved, release, nil
}
