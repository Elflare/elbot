package share

import toolshare "elbot/internal/tool/share"

import (
	"context"

	"elbot/internal/events"
	"elbot/internal/storage"
	
)

func CommitToolState(ctx context.Context, state interface {
	Commit(context.Context, string, toolshare.StateUpdate) (toolshare.StateCommit, events.LogResult, error)
}, row *storage.Session, update toolshare.StateUpdate) (toolshare.StateCommit, events.LogResult, error) {
	if len(update.Tools) == 0 && len(update.Tags) == 0 {
		return toolshare.StateCommit{}, events.LogResult{ResultStatus: events.ResultSkipped}, nil
	}
	result, logResult, err := state.Commit(ctx, row.ID, update)
	if err != nil {
		return toolshare.StateCommit{}, logResult, err
	}
	row.Metadata = result.Metadata
	return result, logResult, nil
}
