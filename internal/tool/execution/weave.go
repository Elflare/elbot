package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	"elbot/internal/operationresult"
	"elbot/internal/storage"
	"elbot/internal/tool/share"
)

// Only interactive confirmations share this slot. Approved tools execute concurrently.
type confirmingDeps struct {
	share.RunnerDeps
	slot  chan struct{} // 整批共用一个确认位置，包含编排中的子调用。
	extra string        // 在确认位置内追加，整批退出时读取。
}

func (d *confirmingDeps) ConfirmToolCall(ctx context.Context, session string, call llm.ToolCallRequest, risk share.RiskAssessment, details []share.ConfirmationDetailBlock) (operationresult.Result[share.ConfirmResult], error) {
	select {
	case d.slot <- struct{}{}:
		defer func() { <-d.slot }()
	case <-ctx.Done():
		return operationresult.Result[share.ConfirmResult]{}, ctx.Err()
	}
	result, err := d.RunnerDeps.ConfirmToolCall(ctx, session, call, risk, details)
	if err == nil && result.Status == operationresult.Success {
		d.extra = joinAssistantText(d.extra, result.Data.Extra)
	}
	return result, err
}

func (m *Manager) weaveNames(ctx context.Context, items []share.DiscoveredTool) ([]string, bool, error) {
	if share.PolicyFromContext(ctx).DisableForegroundTools {
		items = m.BackgroundDiscoveredTools(items)
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		if item.Source == share.SourceKindNative {
			names = append(names, item.Name)
		}
	}
	defs, err := m.Registry.Query(ctx, share.ToolQuery{Names: names, IncludeHidden: true})
	if err != nil {
		return nil, false, err
	}
	names = names[:0]
	open := false
	for _, d := range defs {
		if d.Definition.Name == "tool_weave" {
			open = true
		}
		if d.Definition.Weavable && d.Callable() {
			names = append(names, d.Definition.Name)
		}
	}
	return names, open, nil
}

func (m *Manager) WeaveDescription(ctx context.Context, items []share.DiscoveredTool) (string, error) {
	names, open, err := m.weaveNames(ctx, items)
	if err != nil {
		return "", err
	}
	if len(names) == 0 {
		if !open {
			return "", nil
		}
		return "tool_weave 可调用：发现对应工具后显示", nil
	}
	return "tool_weave 可调用：" + strings.Join(names, "、"), nil
}

func (m *Manager) bindWeave(ctx context.Context, deps share.RunnerDeps, session storage.Session, items []share.DiscoveredTool, parent string, actor contextinfo.Actor) (context.Context, error) {
	names, _, err := m.weaveNames(ctx, items)
	if err != nil {
		return ctx, err
	}
	var seq atomic.Uint64
	call := func(ctx context.Context, name string, args json.RawMessage) (operationresult.Result[string], error) {
		request := llm.ToolCallRequest{ID: fmt.Sprintf("%s/%d", parent, seq.Add(1)), Name: name, Arguments: string(args)}
		snapshot := session
		ctx, err := deps.RefreshExecution(ctx, &snapshot)
		if err != nil {
			return operationresult.Result[string]{}, err
		}
		currentActor := actor
		if refreshed, ok := contextinfo.ActorFromContext(ctx); ok {
			currentActor = refreshed
		}
		return m.callWeave(ctx, deps, &snapshot, items, names, request, currentActor)
	}
	return share.WithWeaveContext(ctx, share.WeaveContext{Names: names, Call: call}), nil
}

func (m *Manager) callWeave(ctx context.Context, deps share.RunnerDeps, session *storage.Session, items []share.DiscoveredTool, names []string, call llm.ToolCallRequest, actor contextinfo.Actor) (result operationresult.Result[string], err error) {
	start := time.Now()
	var outcome executionResult
	var risk string
	defer func() {
		outcome.Error = err
		outcome.Result.Status, outcome.Result.Message = result.Status, result.Message
		m.recordCallCompleted(ctx, session.ID, call, share.SourceKindNative, actor, risk, "tool_weave", start, outcome)
	}()
	if !slices.Contains(names, call.Name) {
		return operationresult.Result[string]{Status: operationresult.Forbidden, Message: "本次不可调用：" + call.Name}, nil
	}
	resolved, err := m.resolve(ctx, call.Name, items, session.Mode)
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	if resolved.Status != operationresult.Success {
		return operationresult.Result[string]{Status: resolved.Status, Message: resolved.Message}, nil
	}
	target := resolved.Data
	if !target.Native.Definition.Weavable {
		return operationresult.Result[string]{Status: operationresult.Forbidden, Message: "工具已停止支持编排：" + call.Name}, nil
	}
	ctx = deps.PrepareToolContext(ctx, session, call)
	prepared, err := m.prepareCall(ctx, target, call)
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	if prepared.Status != operationresult.Success {
		return operationresult.Result[string]{Status: prepared.Status, Message: prepared.Message}, nil
	}
	assessment := prepared.Data.RiskAssessment()
	risk = string(assessment.Level)
	deps.AddToolUse(session.ID, call.Name)
	approval, err := m.confirm(ctx, deps, actor, session.ID, call, target, assessment, prepared.Data.Details())
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	if approval.Status != operationresult.Success {
		return operationresult.Result[string]{Status: approval.Status, Message: approval.Message}, nil
	}
	child, done, err := deps.StartToolRequest(ctx, session.ID, call.Name)
	if err != nil {
		return operationresult.Result[string]{}, err
	}
	defer done()
	child = deps.PrepareToolContext(child, session, call)
	if rejected := m.checkMediaArguments(child, call); rejected != nil {
		outcome = *rejected
	} else {
		outcome = m.executePrepared(child, call, target, prepared.Data, func() { m.recordCallStarted(child, session.ID, call, target.Source, actor, risk, "tool_weave") })
		deliverOutputs(child, deps, &outcome)
	}
	result = operationresult.Result[string]{Status: outcome.Result.Status, Message: outcome.Result.Message}
	if result.Status == operationresult.Success {
		result.Data = llm.SegmentsRawContentText(outcome.Message.Segments)
	}
	return result, outcome.Error
}
