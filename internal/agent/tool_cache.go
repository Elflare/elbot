package agent

import (
	"context"

	"elbot/internal/storage"
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
