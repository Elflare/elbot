package execution

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/llm"
	"elbot/internal/operationresult"
	"elbot/internal/storage"
	toolshare "elbot/internal/tool/share"
	"encoding/json"
	"fmt"
	"time"
)

// The same decoder applies to declared async options across tool entry points.
func asyncMode(info toolshare.Info, raw json.RawMessage, foreground bool) (bool, string) {
	if info.Async == toolshare.AsyncDisabled {
		return false, ""
	}
	var args map[string]json.RawMessage
	if json.Unmarshal(raw, &args) != nil || args == nil {
		return false, "工具参数必须为 JSON 对象"
	}
	async := info.Async == toolshare.AsyncByDefault
	if value, ok := args["async"]; ok {
		switch string(value) {
		case "true":
			async = true
		case "false":
			async = false
		default:
			return false, "async 必须为布尔值"
		}
	}
	if async && !foreground {
		return false, "async 仅限前台 Work 工具调用"
	}
	return async, ""
}

func (result executionResult) record(sid string, call llm.ToolCallRequest, actor contextinfo.Actor, risk string, start, end time.Time) *storage.ToolCallRecord {
	r := &storage.ToolCallRecord{SessionID: sid, ToolCallID: call.ID, ToolName: call.Name, Actor: actor.ActorIdentity, RiskLevel: risk, StartedAt: start, FinishedAt: end, Success: result.Error == nil && result.Result.Status == operationresult.Success}
	if result.Error != nil {
		r.Error = result.Error.Error()
	} else if !r.Success {
		r.Error = result.Result.Message
	}
	return r
}

// Only the actual invocation executes and fires the completion Hook. The later pair is input.
func (m *Manager) runAsync(ctx context.Context, deps toolshare.RunnerDeps, job toolshare.AsyncJob, session *storage.Session, call llm.ToolCallRequest, target resolvedTool, execute func(func()) executionResult, actor contextinfo.Actor, risk, runSource string, withdraw func()) {
	defer job.Done()
	sessionID := session.ID

	start := time.Now()
	result := execute(func() {
		m.recordCallStarted(ctx, sessionID, call, target.Source, actor, risk, runSource)
	})
	if withdraw != nil {
		withdraw()
	} // Retire the prompt before the report can wake another turn.
	m.recordCallCompleted(ctx, sessionID, call, target.Source, actor, risk, runSource, start, result)
	if ctx.Err() == nil {
		if err := result.complete(ctx, deps, session, call, risk); err != nil {
			result.Error = err
			result.Message.Segments = llm.TextSegments(err.Error())
		}
	}
	auditCtx := context.WithoutCancel(ctx)
	if err := m.RecordToolCall(auditCtx, result.record(sessionID, call, actor, risk, start, time.Now())); err != nil {
		m.Log(auditCtx, newRunRecord(auditCtx, sessionID, "async_tool_record_failed", err))
	}
	report := call
	report.ID = "call_" + storage.NewID()
	state := string(result.Result.Status)
	if result.Error != nil {
		state = "error"
	}
	segments := append(llm.TextSegments(fmt.Sprintf("任务 %s 回报（%s），非新执行。\n", job.ID, state)), result.Message.Segments...)
	if err := job.Deliver(report, delivery.ToolResult{Success: result.Error == nil && result.Result.Status == operationresult.Success, Segments: segments, Blocks: result.Changes}); err != nil {
		m.Log(ctx, newRunRecord(ctx, sessionID, "async_tool_delivery_failed", err))
	}
}
