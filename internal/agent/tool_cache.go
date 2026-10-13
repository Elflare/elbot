package agent

import (
	"context"

	"elbot/internal/events"
	"elbot/internal/storage"
	"elbot/internal/toolrun"
)

func commitToolState(ctx context.Context, state *toolrun.StateService, row *storage.Session, update toolrun.StateUpdate) (toolrun.StateCommit, events.LogResult, error) {
	if len(update.Tools) == 0 && len(update.Tags) == 0 && len(update.ShownRuleCardFormats) == 0 {
		return toolrun.StateCommit{}, events.LogResult{ResultStatus: events.ResultSkipped}, nil
	}
	result, logResult, err := state.Commit(ctx, row.ID, update)
	if err != nil {
		return toolrun.StateCommit{}, logResult, err
	}
	row.Metadata = result.Metadata
	return result, logResult, nil
}
