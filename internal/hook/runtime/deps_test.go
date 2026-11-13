package runtime

import toolshare "elbot/internal/tool/share"

import (
	"context"
	"elbot/internal/events"
	"elbot/internal/hook/share"
	"elbot/internal/operationresult"
	"elbot/internal/tool"
	"elbot/internal/tool/execution"
	"elbot/internal/tool/registry"
	"testing"
	"time"
)

// Tool bridge tests supply an active event without running a worker process.
func testToolContext(t *testing.T, w *worker, ctx context.Context, event share.Event) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	token := randomID("ctx")
	w.manager.mu.Lock()
	w.manager.tokens[token] = invocation{Worker: w, Event: event, Context: ctx, Cancel: cancel}
	w.manager.mu.Unlock()
	t.Cleanup(cancel)
	return token
}

func testLog(ctx context.Context, record events.LogRecord) {
	events.Log(context.WithoutCancel(ctx), record)
}

func testNotify(ctx context.Context, notice events.Notification) {
	events.Notify(context.WithoutCancel(ctx), notice)
}

type testTools struct{ *registry.Registry }

func (t testTools) QueryTools(ctx context.Context, query tool.ToolQuery) ([]tool.ToolInfo, error) {
	definitions, err := t.Query(ctx, toolshare.ToolQuery{Scope: toolshare.QueryScope(query.Scope), Names: query.Names, Kind: query.Kind, IncludeHidden: query.IncludeHidden, IncludeDiscover: query.IncludeDiscover})
	if err != nil {
		return nil, err
	}
	infos := make([]tool.ToolInfo, len(definitions))
	for i, value := range definitions {
		info := value.Definition
		infos[i] = tool.ToolInfo{Definition: tool.Info{Name: info.Name, Description: info.Description, Risk: info.Risk, Source: tool.Source(info.Source), Hidden: info.Hidden, SuperadminOnly: info.SuperadminOnly, APITypes: info.APITypes, ForegroundOnly: info.ForegroundOnly, OwnerScoped: info.OwnerScoped, Tags: info.Tags, DependsOn: info.DependsOn, NeedsElyphRules: info.NeedsElyphRules}, Parameters: value.Parameters}
	}
	return infos, nil
}

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
