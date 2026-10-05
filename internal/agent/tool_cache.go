package agent

import (
	"context"

	"elbot/internal/storage"
	"elbot/internal/tool"
	"elbot/internal/toolrun"
)

func commitToolState(ctx context.Context, state *toolrun.StateService, row *storage.Session, update toolrun.StateUpdate) (toolrun.StateCommit, error) {
	if len(update.Tools) == 0 && len(update.Tags) == 0 && len(update.ShownRuleCardFormats) == 0 {
		return toolrun.StateCommit{}, nil
	}
	result, err := state.Commit(ctx, row.ID, update)
	if err != nil {
		return toolrun.StateCommit{}, err
	}
	row.Metadata = result.Metadata
	return result, nil
}

func cachedToolsForSession(ctx context.Context, service *toolrun.StateService, registry *tool.Registry, row *storage.Session) ([]toolrun.CachedTool, error) {
	if row == nil {
		return nil, nil
	}
	state, err := service.Snapshot(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	return state.CachedTools(registry, isBackgroundSession(row)), nil
}
