package skill

import (
	"context"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/operationresult"
	"elbot/internal/tool/discovery"
	"elbot/internal/tool/share"
)

func withSkillAvailability(t *testing.T, ctx context.Context, registry interface {
	Query(context.Context, share.ToolQuery) ([]share.ToolDefinition, error)
}) context.Context {
	t.Helper()
	availability, err := discovery.NewResolver(registry).Availability(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return contextinfo.WithToolAvailability(ctx, availability)
}

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
