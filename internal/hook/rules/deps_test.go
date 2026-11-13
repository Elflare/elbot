package rules

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/operationresult"
	"elbot/internal/tool"
	"elbot/internal/tool/execution"
	"elbot/internal/tool/registry"
)

func testLog(ctx context.Context, record events.LogRecord) {
	events.Log(context.WithoutCancel(ctx), record)
}

func testNotify(ctx context.Context, notice events.Notification) {
	events.Notify(context.WithoutCancel(ctx), notice)
}

type testTools struct{ *registry.Registry }

func (t testTools) CallTool(ctx context.Context, call tool.CallRequest) (operationresult.Result[*tool.Result], string, error) {
	m := execution.Manager{ManagerDeps: execution.ManagerDeps{Registry: t.Registry}}
	result, log, err := m.CallTool(ctx, toolshare.CallRequest{ID: call.ID, Name: call.Name, Arguments: call.Arguments})
	if err != nil {
		return operationresult.Result[*tool.Result]{}, log, err
	}
	outcome := operationresult.Result[*tool.Result]{Status: result.Status, Message: result.Message}
	if result.Status != operationresult.Success || result.Data == nil {
		return outcome, log, nil
	}
	value := result.Data
	out := &tool.Result{NeedsElyphRules: value.NeedsElyphRules, Content: value.Content, Segments: value.Segments, Warnings: value.Warnings, Data: value.Data, Metadata: value.Metadata, Outputs: value.Outputs}
	if value.DiscoveryText != nil {
		out.DiscoveryText = &tool.DiscoveryText{Summary: value.DiscoveryText.Summary, Details: value.DiscoveryText.Details}
	}
	outcome.Data = out
	return outcome, log, nil
}
