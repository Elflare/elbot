package rules

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/tool"
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

func (t testTools) ToolInfo(ctx context.Context, name string) (tool.Info, bool, error) {
	value, ok := t.Get(name)
	if !ok {
		return tool.Info{}, false, nil
	}
	info := value.Info()
	return tool.Info{Name: info.Name, Description: info.Description, Risk: info.Risk, Source: tool.Source(info.Source), Hidden: info.Hidden, SuperadminOnly: info.SuperadminOnly, APITypes: info.APITypes, ForegroundOnly: info.ForegroundOnly, OwnerScoped: info.OwnerScoped, Tags: info.Tags, DependsOn: info.DependsOn, NeedsElyphRules: info.NeedsElyphRules}, true, nil
}

func (t testTools) CallTool(ctx context.Context, call tool.CallRequest) (*tool.Result, string, error) {
	value, _ := t.Get(call.Name)
	prepared, err := value.PrepareCall(ctx, toolshare.CallRequest{ID: call.ID, Name: call.Name, Arguments: call.Arguments})
	if err != nil {
		return nil, "", err
	}
	result, log, err := prepared.Call(ctx)
	if result == nil {
		return nil, log, err
	}
	out := &tool.Result{NeedsElyphRules: result.NeedsElyphRules, Content: result.Content, Segments: result.Segments, Warnings: result.Warnings, Data: result.Data, Metadata: result.Metadata, Outputs: result.Outputs}
	if result.DiscoveryText != nil {
		out.DiscoveryText = &tool.DiscoveryText{Summary: result.DiscoveryText.Summary, Details: result.DiscoveryText.Details}
	}
	return out, log, err
}
