package agent

import (
	"context"
	"fmt"

	"elbot/internal/delivery"
)

// Resolve only the send copy; Hook events and queued outputs keep media IDs.
func (a *Agent) resolveMediaOutputs(ctx context.Context, outputs []delivery.Output) ([]delivery.Output, func(), error) {
	resolved := append([]delivery.Output(nil), outputs...)
	var cleanups []func()
	cleanup := func() {
		for _, fn := range cleanups {
			fn()
		}
	}
	if err := delivery.ValidateOutputs(outputs); err != nil {
		return nil, cleanup, err
	}
	for i, out := range resolved {
		if out.Source.MediaID == "" {
			continue
		}
		if a.media == nil {
			cleanup()
			return nil, func() {}, fmt.Errorf("media center is not configured")
		}
		source, release, err := a.media.ResolveForOutput(ctx, out.Source.MediaID)
		if err != nil {
			cleanup()
			return nil, func() {}, err
		}
		cleanups = append(cleanups, release)
		resolved[i].Source = source
		if resolved[i].Name == "" {
			metadata, err := a.media.Metadata(ctx, out.Source.MediaID)
			if err != nil {
				cleanup()
				return nil, func() {}, err
			}
			resolved[i].Name = metadata.Name
		}
	}
	return resolved, cleanup, nil
}
