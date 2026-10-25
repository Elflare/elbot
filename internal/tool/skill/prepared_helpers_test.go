package skill

import (
	"context"
	"elbot/internal/tool/share"
)

// callTool 让业务用例经过真实的准备入口，不保留旧 Tool.Call 契约。
func callTool(ctx context.Context, tool share.Tool, req share.CallRequest) (*share.Result, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err.Error(), err
	}
	prepared, err := tool.PrepareCall(ctx, req)
	if err != nil {
		return nil, share.TruncateLogContent(err.Error(), 1024), err
	}
	return prepared.Call(ctx)
}
