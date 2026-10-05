package agent

import (
	"context"

	"elbot/internal/config"
	"elbot/internal/modelmgr"
	"elbot/internal/storage"
)

func modelSelectionForTurn(ctx context.Context, models *modelmgr.Service, session *storage.Session) modelmgr.Selection {
	mode := storage.SessionModeWork
	if session != nil && session.Mode != "" && session.Mode != storage.SessionModeBackground {
		mode = session.Mode
	}
	selection := models.ResolveMode(mode).ModelSelection
	if override, ok := ctx.Value(backgroundModelSelectionKey{}).(config.ModelSelection); ok {
		if override.Provider != "" {
			selection.Provider = override.Provider
		}
		if override.Model != "" {
			selection.Model = override.Model
		}
	}
	return models.Resolve(selection)
}
