package rules

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/tool"
	"elbot/internal/tool/execution"
	"elbot/internal/tool/registry"
)

func testLog(ctx context.Context, record events.LogRecord) {
	_ = events.EmitLog(context.WithoutCancel(ctx), record)
}

func testNotify(ctx context.Context, notice events.Notification) error {
	_, err := events.NotificationRequested.EmitAsync(context.WithoutCancel(ctx), notice)
	return err
}

type testTools struct{ *registry.Registry }

func (t testTools) CallTool(ctx context.Context, call tool.CallRequest) (*tool.Result, string, error) {
	m := execution.Manager{ManagerDeps: execution.ManagerDeps{Registry: t.Registry}}
	result, log, err := m.CallTool(ctx, toolshare.CallRequest{ID: call.ID, Name: call.Name, Arguments: call.Arguments})
	if result == nil {
		return nil, log, err
	}
	out := &tool.Result{NeedsElyphRules: result.NeedsElyphRules, Content: result.Content, Segments: result.Segments, Warnings: result.Warnings, Data: result.Data, Metadata: result.Metadata, Outputs: result.Outputs}
	if result.DiscoveryText != nil {
		out.DiscoveryText = &tool.DiscoveryText{Summary: result.DiscoveryText.Summary, Details: result.DiscoveryText.Details}
	}
	return out, log, err
}
