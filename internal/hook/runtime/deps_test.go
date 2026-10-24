package runtime

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/tool"
)

func testLog(ctx context.Context, record events.LogRecord) {
	_ = events.EmitLog(context.WithoutCancel(ctx), record)
}

func testNotify(ctx context.Context, notice events.Notification) error {
	_, err := events.NotificationRequested.EmitAsync(context.WithoutCancel(ctx), notice)
	return err
}

type testTools struct{ *tool.Registry }

func (t testTools) ToolInfo(name string) (toolshare.Info, bool) {
	value, ok := t.Get(name)
	if !ok {
		return toolshare.Info{}, false
	}
	return value.Info(), true
}

func (t testTools) ToolSchema(name string) (llm.ToolSchema, bool) {
	value, ok := t.Get(name)
	if !ok {
		return llm.ToolSchema{}, false
	}
	return value.Schema(), true
}

func (t testTools) CallTool(ctx context.Context, call toolshare.CallRequest) (*toolshare.Result, string, error) {
	value, _ := t.Get(call.Name)
	return value.Call(ctx, call)
}
