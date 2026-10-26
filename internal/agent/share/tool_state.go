// 提交 Tool 准备的完整声明，并同步调用方持有的会话元数据。
package share

import (
	"context"
	"elbot/internal/tool"

	"elbot/internal/events"
	"elbot/internal/storage"
)

func CommitToolState(ctx context.Context, state interface {
	Commit(context.Context, string, tool.StateUpdate) (tool.StateCommit, events.LogResult, error)
}, row *storage.Session, update tool.StateUpdate) (tool.StateCommit, events.LogResult, error) {
	if len(update.Tools) == 0 && len(update.Tags) == 0 {
		return tool.StateCommit{}, events.LogResult{ResultStatus: events.ResultSkipped}, nil
	}
	result, logResult, err := state.Commit(ctx, row.ID, update)
	if err != nil {
		return tool.StateCommit{}, logResult, err
	}
	row.Metadata = result.Metadata
	return result, logResult, nil
}
