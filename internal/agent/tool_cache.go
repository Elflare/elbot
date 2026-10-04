package agent

import (
	"context"
	"elbot/internal/storage"
	"elbot/internal/toolrun"
)

func (a *Agent) commitToolState(ctx context.Context, row *storage.Session, update toolrun.StateUpdate) (toolrun.StateCommit, error) {
	if len(update.Tools) == 0 && len(update.Tags) == 0 && len(update.ShownRuleCardFormats) == 0 {
		return toolrun.StateCommit{}, nil
	}
	result, err := a.toolState.Commit(ctx, row.ID, update)
	if err != nil {
		return toolrun.StateCommit{}, err
	}
	row.Metadata = result.Metadata
	return result, nil
}

func (a *Agent) cachedToolsForSession(ctx context.Context, row *storage.Session) ([]toolrun.CachedTool, error) {
	if row == nil {
		return nil, nil
	}
	state, err := a.toolState.Snapshot(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	return state.CachedTools(a.toolRuntime.registry, isBackgroundSession(row)), nil
}
