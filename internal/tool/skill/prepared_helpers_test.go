package skill

import (
	"context"
	"elbot/internal/operationresult"
	"elbot/internal/tool/share"
)

// callTool 让业务用例经过真实的准备入口，不保留旧 Tool.Call 契约。
func callTool(ctx context.Context, tool share.ToolDefinition, req share.CallRequest) (operationresult.Result[*share.Result], string, error) {
	if err := ctx.Err(); err != nil {
		return operationresult.Result[*share.Result]{}, err.Error(), err
	}
	prepared, err := tool.PrepareCall(ctx, req)
	if err != nil {
		return operationresult.Result[*share.Result]{}, share.TruncateLogContent(err.Error(), 1024), err
	}
	if prepared.Status != operationresult.Success {
		return operationresult.Result[*share.Result]{Status: prepared.Status, Message: prepared.Message}, prepared.Message, nil
	}
	return prepared.Data.Call(ctx)
}
