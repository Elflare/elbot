package tool

import (
	"context"
	"elbot/internal/fileops"
	"elbot/internal/memory/resident"
	"elbot/internal/operationresult"
	"elbot/internal/tool/builtin"
	"elbot/internal/tool/share"
	"path/filepath"
	"testing"
	"time"
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

func preparedRisk(ctx context.Context, tool share.ToolDefinition, req share.CallRequest) (operationresult.Result[share.RiskAssessment], error) {
	prepared, err := tool.PrepareCall(ctx, req)
	if err != nil {
		return operationresult.Result[share.RiskAssessment]{}, err
	}
	if prepared.Status != operationresult.Success {
		return operationresult.Result[share.RiskAssessment]{Status: prepared.Status, Message: prepared.Message}, nil
	}
	return operationresult.Result[share.RiskAssessment]{Status: operationresult.Success, Data: prepared.Data.RiskAssessment()}, nil
}

func builtinTestTool(t *testing.T, name string, deps builtin.Deps) share.ToolDefinition {
	t.Helper()
	deps.FileRollback = fileops.NewService(nil)
	deps.ResidentMemory = residentMemoryBridge{store: resident.NewStore(filepath.Join(t.TempDir(), "memory.toml"))}
	deps.LongMemoryDir = t.TempDir()
	deps.Now = time.Now
	for _, definition := range builtin.NewTools(deps) {
		if definition.Definition.Name == name {
			return definition
		}
	}
	t.Fatalf("builtin %q not found", name)
	return share.ToolDefinition{}
}
