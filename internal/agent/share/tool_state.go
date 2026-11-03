// 提交 Tool 准备的完整声明，并同步调用方持有的会话元数据。
package share

import (
	"context"
	"elbot/internal/tool"

	"elbot/internal/storage"
)

func CommitToolState(ctx context.Context, state interface {
	Commit(context.Context, string, tool.StateUpdate) (tool.StateCommit, error)
}, row *storage.Session, update tool.StateUpdate) (tool.StateCommit, error) {
	if len(update.Tools) == 0 && len(update.Tags) == 0 {
		return tool.StateCommit{}, nil
	}
	result, err := state.Commit(ctx, row.ID, update)
	if err != nil {
		return tool.StateCommit{}, err
	}
	row.Metadata = result.Metadata
	return result, nil
}
