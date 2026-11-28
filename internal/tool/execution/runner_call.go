package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/operationresult"
	"elbot/internal/storage"
	toolshare "elbot/internal/tool/share"
	"elbot/internal/workspace"
)

// startCall 在协调器上顺序准备和确认。单项拒绝入队，批次错误或停止直接返回。
func (b *runBatch) startCall(index int) toolshare.RunResult {
	callCtx := b.ctx
	actor := b.req.Actor
	if current, ok := contextinfo.ActorFromContext(callCtx); ok {
		actor = current
	}
	// Session snapshots keep later handoffs and discovery writes out of workers.
	snapshot := *b.req.Session
	task := &b.calls[index]
	task.startedAt = time.Now()
	task.actor = actor
	call := task.call
	var err error
	if waitErr := b.await(func() { call, err = b.deps.PrepareToolCall(callCtx, &snapshot, call) }); waitErr != nil {
		return toolshare.RunResult{Err: waitErr}
	}
	if err != nil {
		return toolshare.RunResult{Err: err}
	}
	task.call = call
	if err := b.req.Committer.Prepared(b.ctx, index, call); err != nil {
		return toolshare.RunResult{Err: err}
	}
	resolved, err := b.manager.resolve(callCtx, call.Name, b.req.DiscoveredTools, snapshot.Mode)
	if err != nil {
		return toolshare.RunResult{Err: err}
	}
	runSource := "foreground"
	if snapshot.Mode == storage.SessionModeBackground {
		runSource = snapshot.TaskType
	}
	finish := func(result executionResult, audit bool) {
		task.result = result
		task.finishedAt = time.Now()
		if audit {
			b.manager.recordCallCompleted(callCtx, b.sessionID, call, resolved.Data.Source, actor, task.risk, runSource, task.startedAt, result)
		}
		b.ready <- index
	}
	if resolved.Status != operationresult.Success {
		finish(executionRejected(call, resolved.Status, resolved.Message), true)
		return toolshare.RunResult{}
	}
	resolvedTool := resolved.Data
	toolCtx := b.deps.PrepareToolContext(callCtx, &snapshot, call)
	var prepared operationresult.Result[*toolshare.PreparedCall]
	if waitErr := b.await(func() { prepared, err = prepareCall(toolCtx, resolvedTool, call) }); waitErr != nil {
		return toolshare.RunResult{Err: waitErr}
	}
	if err != nil {
		finish(executionError(call, err), true)
		return toolshare.RunResult{}
	}
	if prepared.Status != operationresult.Success {
		finish(executionRejected(call, prepared.Status, prepared.Message), true)
		return toolshare.RunResult{}
	}
	async := false
	if resolvedTool.Native != nil {
		var reason string
		async, reason = asyncMode(resolvedTool.Native.Definition, json.RawMessage(call.Arguments), snapshot.Mode == storage.SessionModeWork)
		if reason != "" {
			finish(executionRejected(call, operationresult.InvalidArguments, reason), false)
			return toolshare.RunResult{}
		}
	}
	preparedCall := prepared.Data
	assessment := preparedCall.RiskAssessment()
	task.risk = string(assessment.Level)
	b.deps.AddToolUse(b.sessionID, call.Name)
	var confirmation operationresult.Result[toolshare.ConfirmResult]
	waitErr := b.await(func() {
		confirmation, err = b.manager.confirm(toolCtx, b.deps, actor, b.sessionID, call, resolvedTool, assessment, preparedCall.Details())
	})
	if extra := strings.TrimSpace(confirmation.Data.Extra); err == nil && confirmation.Status == operationresult.Success && extra != "" {
		b.confirmationExtra = append(b.confirmationExtra, extra)
	}
	if waitErr != nil {
		return toolshare.RunResult{Err: waitErr}
	}
	if err != nil {
		return toolshare.RunResult{Err: err}
	}
	switch confirmation.Status {
	case operationresult.Timeout, operationresult.Canceled, operationresult.Conflict:
		return toolshare.RunResult{Stopped: true}
	}
	if confirmation.Status != operationresult.Success {
		finish(executionRejected(call, confirmation.Status, confirmation.Message), true)
		return toolshare.RunResult{}
	}
	if rejected := b.manager.checkMediaArguments(toolCtx, call); rejected != nil {
		finish(*rejected, false)
		return toolshare.RunResult{}
	}
	var withdraw func()
	if prompt := preparedCall.Prompt; prompt != nil {
		output, publish, cancelPrompt := prompt(b.sessionID)
		sent, err := b.deps.SendOutputs(toolCtx, []delivery.Output{output})
		if err != nil {
			finish(executionError(call, err), false)
			return toolshare.RunResult{}
		}
		if sent.Status != operationresult.Success {
			finish(executionResult{
				Message: toolMessage(call.Name, call.ID, sent.Message),
				Result:  operationresult.Result[*toolshare.Result]{Status: sent.Status, Message: sent.Message},
			}, false)
			return toolshare.RunResult{}
		}
		publish()
		withdraw = cancelPrompt
	}
	return toolshare.RunResult{Err: b.launch(index, toolCtx, &snapshot, resolvedTool, preparedCall, async, runSource, withdraw)}
}

func (b *runBatch) launch(index int, toolCtx context.Context, snapshot *storage.Session, resolvedTool resolvedTool, preparedCall *toolshare.PreparedCall, async bool, runSource string, withdraw func()) error {
	task := &b.calls[index]
	call := task.call
	var job toolshare.AsyncJob
	var runToolCtx context.Context
	var done func()
	var err error
	if async {
		job, err = b.req.Jobs.StartAsyncTool(toolCtx, b.sessionID, call.Name)
		runToolCtx, done = job.Context, job.Done
	} else {
		runToolCtx, done, err = b.deps.StartToolRequest(toolCtx, b.sessionID, call.Name)
	}

	if err != nil {
		if withdraw != nil {
			withdraw()
		}
		return err
	}
	if withdraw != nil {
		release := done
		done = func() { withdraw(); release() }
	}
	runToolCtx = b.deps.PrepareToolContext(runToolCtx, snapshot, call)
	var children weaveChanges
	if call.Name == "tool_weave" {
		runToolCtx, err = b.manager.bindWeave(runToolCtx, b.deps, *snapshot, b.req.DiscoveredTools, call.ID, task.actor, &children)
		if err != nil {
			done()
			return err
		}
	}
	execute := func(onStarted func()) executionResult {
		result := b.manager.executePrepared(runToolCtx, call, resolvedTool, preparedCall, onStarted)
		result.Changes = append(result.Changes, children.blocks...)
		return result
	}
	if async {
		// Save acceptance before starting the worker. A failed commit cannot leak a job.
		receipt := "已受理异步任务 " + job.ID + "；完成后自动回报。"
		task.result = executionResult{Message: toolMessage(call.Name, call.ID, receipt), Result: operationresult.Result[*toolshare.Result]{Status: operationresult.Success}}
		if err := b.commit(index, true); err != nil {
			task.result = executionResult{}
			done()
			return err
		}
		go b.manager.runAsync(runToolCtx, b.deps, job, snapshot, call, resolvedTool, execute, task.actor, task.risk, runSource, withdraw)
		return nil
	}
	b.workers.Go(func() {
		startedAt := time.Now()
		result := execute(func() {
			startedAt = time.Now()
			task.startedAt = startedAt
			b.manager.recordCallStarted(runToolCtx, b.sessionID, call, resolvedTool.Source, task.actor, task.risk, runSource)
		})
		b.manager.recordCallCompleted(runToolCtx, b.sessionID, call, resolvedTool.Source, task.actor, task.risk, runSource, startedAt, result)
		task.finishedAt = time.Now()
		done()
		task.result = result
		b.ready <- index
	})
	return nil
}

func (m *Manager) confirm(ctx context.Context, deps toolshare.RunnerDeps, actor contextinfo.Actor, sessionID string, call llm.ToolCallRequest, resolvedTool resolvedTool, assessment toolshare.RiskAssessment, detail []toolshare.ConfirmationDetailBlock) (operationresult.Result[toolshare.ConfirmResult], error) {
	if resolvedTool.Source == toolshare.SourceKindELwisp {
		return operationresult.Result[toolshare.ConfirmResult]{Status: operationresult.Success}, nil
	}
	info := resolvedTool.Native.Definition
	permission := m.Permission.CheckTool(actor, call.Name, assessment.Level, info.OwnerScoped, info.SuperadminOnly, toolshare.PolicyFromContext(ctx).AutoConfirmShell)
	if permission.Reason == "background_shell_auto_confirmed" || permission.Reason == "background_shell_rejected" {
		facts, _ := contextinfo.ExecutionFromContext(ctx)
		scope, _ := workspace.PathScopeFromContext(ctx)
		content := map[string]string{"task_type": facts.TaskType, "tool": call.Name, "risk": string(assessment.Level), "sandbox_dir": scope.Dir, "arguments": call.Arguments}
		for key, value := range content {
			content[key] = toolshare.TruncateLogContent(value, 2048)
		}
		data, _ := json.Marshal(content)
		resultStatus := events.ResultSucceeded
		if !permission.Allowed {
			resultStatus = events.ResultRejected
		}
		record := events.LogRecord{Category: events.LogCategoryAudit, Level: slog.LevelInfo, EventName: permission.Reason, Module: "tool", Type: events.ContentJSON, Content: string(data), ResultStatus: resultStatus}
		record.Identity = events.LogIdentityFromContext(ctx)
		record.Identity.SessionID = sessionID
		m.Log(ctx, record)
	}
	if !permission.Allowed {
		switch permission.Reason {
		case "tool_requires_superadmin":
			deps.AuditToolDenied(ctx, sessionID, call, assessment.Level, "tool_requires_superadmin")
			return operationresult.Result[toolshare.ConfirmResult]{Status: operationresult.Forbidden, Message: fmt.Sprintf("tool call %s denied: requires superadmin role", call.Name)}, nil
		case "background_shell_rejected":
			return operationresult.Result[toolshare.ConfirmResult]{Status: operationresult.Forbidden, Message: "后台沙盒禁止执行 critical 风险 shell 命令。请改用相对路径，把文件限制在当前 sandbox 目录内，避免绝对路径、.. 逃逸和危险命令；若仍无法完成，请在最终报告中说明阻塞原因。"}, nil
		default:
			deps.AuditToolDenied(ctx, sessionID, call, assessment.Level, "tool_risk_above_allowed_level")
			return operationresult.Result[toolshare.ConfirmResult]{Status: operationresult.Forbidden, Message: fmt.Sprintf("tool call %s denied: risk %s is above your allowed tool level", call.Name, assessment.Level)}, nil
		}
	}
	if permission.NeedsConfirmation {
		return deps.ConfirmToolCall(ctx, sessionID, call, assessment, detail)
	}
	return operationresult.Result[toolshare.ConfirmResult]{Status: operationresult.Success}, nil
}
