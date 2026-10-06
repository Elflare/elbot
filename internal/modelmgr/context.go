package modelmgr

import (
	"context"

	"elbot/internal/config"
	"elbot/internal/storage"
)

type selectionOverrideKey struct{}

func WithSelectionOverride(ctx context.Context, selected config.ModelSelection) context.Context {
	return context.WithValue(ctx, selectionOverrideKey{}, selected)
}

func SelectionForTurn(ctx context.Context, models *Service, row *storage.Session) Selection {
	mode := storage.SessionModeWork
	if row != nil && row.Mode != "" && row.Mode != storage.SessionModeBackground {
		mode = row.Mode
	}
	selected := models.ResolveMode(mode).ModelSelection
	if override, ok := ctx.Value(selectionOverrideKey{}).(config.ModelSelection); ok {
		if override.Provider != "" {
			selected.Provider = override.Provider
		}
		if override.Model != "" {
			selected.Model = override.Model
		}
	}
	return models.Resolve(selected)
}

func SelectionOverrideFromContext(ctx context.Context) config.ModelSelection {
	selected, _ := ctx.Value(selectionOverrideKey{}).(config.ModelSelection)
	return selected
}
