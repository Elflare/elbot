// 编排工具批次的准备、确认、执行和结果处理。
package execution

import toolshare "elbot/internal/tool/share"

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/elyph"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/storage"
	"elbot/internal/workspace"
)

// Run 顺序准备与确认、并发执行，并由单一协调器提交结果。
func (m *Manager) Run(ctx context.Context, deps toolshare.RunnerDeps, req toolshare.RunRequest) toolshare.RunResult {
	if req.Session.Mode == storage.SessionModeChat {
		return toolshare.RunResult{}
	}
	sessionID := req.Session.ID
	state, err := m.DecodeState(req.Session.Metadata)
	if err != nil {
		return toolshare.RunResult{Err: err}
	}
	elyphRulesShown := state.ElyphRulesShown
	preparedCalls := append([]llm.ToolCallRequest(nil), req.Calls...)
	ctx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	// The coordinator owns session state; workers return independent outcomes
	// through a shared completion queue.
	tasks := make([]struct {
		risk       string
		actor      contextinfo.Actor
		startedAt  time.Time
		result     executionResult
		finishedAt time.Time
	}, len(req.Calls))
	head := deps.ToolCallMessage(sessionID, req.AssistantText, req.AssistantRawText, req.Calls)
	err = req.Committer.Begin(ctx, &head)

	if err != nil {
		return toolshare.RunResult{Err: err}
	}
	ready := make(chan int, len(req.Calls))
	completed := 0
	complete := func(index int) error {
		defer func() { completed++ }()
		task := &tasks[index]
		result := task.result
		if ctx.Err() != nil {
			return ctx.Err()
		}
		updated, err := deps.RefreshExecution(ctx, req.Session)
		if err != nil {
			return err
		}
		ctx = updated
		call := preparedCalls[index]
		actor := task.actor
		rawRecord := &storage.ToolCallRecord{SessionID: sessionID, ToolCallID: call.ID, ToolName: call.Name, Actor: actor.ActorIdentity, RiskLevel: task.risk, StartedAt: task.startedAt, FinishedAt: task.finishedAt, Success: result.Error == nil}
		if result.Error != nil {
			rawRecord.Error = result.Error.Error()
		}
		metadata := storage.SessionMetadata{}
		if result.Result != nil && len(result.Result.Outputs) > 0 {
			err := deps.SendOutputs(ctx, result.Result.Outputs)
			if err != nil {
				events.LogAndNotify(ctx, events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelWarn, EventName: "tool_output_failed", Module: "tool", Content: err.Error(), Identity: events.LogIdentityFromContext(ctx), ResultStatus: events.LogStatus(err)}, events.NewNotification(ctx, slog.LevelWarn, err.Error()))
				result.Message.Segments = append(result.Message.Segments, llm.TextSegments(fmt.Sprintf("\ntool call %s failed: output: %v", call.Name, err))...)
			}
		}
		if result.ExecutionStarted {
			segments, hookErr := deps.CompleteToolCall(ctx, req.Session, call, task.risk, result.Message.Segments, result.Error)
			if hookErr != nil {
				return hookErr
			} else {
				result.Message.Segments = segments
			}
		}
		ruleCard := ""
		if result.Result != nil {
			ruleCard = elyph.RuleCardIfNeeded(result.Result.NeedsElyphRules, elyphRulesShown)
			var parts []string
			if result.Result.DiscoveryText != nil && result.Result.DiscoveryText.Summary != "" {
				parts = append(parts, result.Result.DiscoveryText.Summary)
			}
			if ruleCard != "" {
				parts = append(parts, ruleCard)
			}
			if len(parts) > 0 {
				result.Message.Segments = append(llm.TextSegments(strings.Join(parts, "\n\n")+"\n\n"), result.Message.Segments...)
			}
		}
		if call.Name == "discover_tool" && result.Result != nil {
			update, err := m.DiscoveryStateUpdate(contextinfo.WithActor(ctx, actor), result.Result)
			if err != nil {
				return err
			}
			metadata, err = m.PrepareUpdate(req.Session.Metadata, update)
			if err != nil {
				return err
			}
		}
		stored := deps.ToolResultMessage(sessionID, result.Message)
		if ruleCard != "" {
			fields, err := storage.DecodeSessionMetadata(stored.Metadata)
			if err != nil {
				return err
			}
			_ = fields.Set("elyph_rules_shown", true)
			stored.Metadata, _ = fields.Encode()
		}
		err = req.Committer.Result(ctx, index, call, result.Message, &storage.ToolResultCommit{Result: &stored, Record: rawRecord, Metadata: metadata})

		if err != nil {
			return err
		}
		deps.PublishToolResult(context.WithoutCancel(ctx), call.ID, delivery.ToolResult{
			Name: call.Name, Success: result.Error == nil, Segments: result.Message.Segments,
		})
		if ruleCard != "" {
			elyphRulesShown = true
			_ = metadata.Set("elyph_rules_shown", true)
		}
		if len(metadata) != 0 {
			fields, err := storage.DecodeSessionMetadata(req.Session.Metadata)
			if err != nil {
				return err
			}
			for key, value := range metadata {
				fields[key] = value
			}
			req.Session.Metadata, _ = fields.Encode()
		}
		return nil
	}
	// Blocking preparation receives a detached snapshot; completed tools can be
	// persisted while a later Hook or confirmation is waiting.
	await := func(run func()) error {
		done := make(chan struct{})
		go func() { defer close(done); run() }()
		for {
			select {
			case <-done:
				return nil
			case index := <-ready:
				if err := complete(index); err != nil {
					cancel()
					<-done
					return err
				}
			case <-ctx.Done():
				<-done
				return ctx.Err()
			}
		}
	}
	var confirmationExtra string
	batchPreviewSent := sendBatchToolPreview(ctx, deps, req)
	for index, original := range req.Calls {
		if ctx.Err() != nil {
			return toolshare.RunResult{Stopped: true, Err: ctx.Err()}
		}
		updated, err := deps.RefreshExecution(ctx, req.Session)
		if err != nil {
			return toolshare.RunResult{Err: err}
		}
		ctx = updated
		actor := req.Actor
		if current, ok := contextinfo.ActorFromContext(ctx); ok {
			actor = current
		}
		// Session snapshots keep later handoffs and discovery writes out of workers.
		snapshot := *req.Session
		task := &tasks[index]
		task.startedAt = storage.Now()
		task.actor = actor
		fail := func(call llm.ToolCallRequest, err error, content string) {
			task.result = executionResult{Message: toolMessage(call.Name, call.ID, content), Error: err}
			task.finishedAt = storage.Now()
			ready <- index
		}
		var call llm.ToolCallRequest
		prepareCtx := ctx
		if waitErr := await(func() { call, err = deps.PrepareToolCall(prepareCtx, &snapshot, original) }); waitErr != nil {
			return toolshare.RunResult{Err: waitErr}
		}
		if err != nil {
			return toolshare.RunResult{Err: err}
		}
		preparedCalls[index] = call
		err = req.Committer.Prepared(ctx, index, call)

		if err != nil {
			return toolshare.RunResult{Err: err}
		}
		resolved := m.resolve(ctx, call.Name, req.DiscoveredTools, snapshot.Mode)
		runSource := "foreground"
		if snapshot.Mode == storage.SessionModeBackground {
			runSource = snapshot.TaskType
		}
		if resolved.Error != nil {
			startedAt := time.Now()
			task.result = m.executePrepared(ctx, call, resolved, nil, nil)
			m.recordCallCompleted(ctx, sessionID, call, resolved.Source, actor, "", runSource, startedAt, task.result)
			task.finishedAt = storage.Now()
			ready <- index
			continue
		}
		toolCtx := deps.PrepareToolContext(ctx, &snapshot, call)
		var prepared *toolshare.PreparedCall
		if waitErr := await(func() { prepared, err = m.prepareCall(toolCtx, resolved, call) }); waitErr != nil {
			return toolshare.RunResult{Err: waitErr}
		}
		if err != nil {
			task.result = executionError(call, err)
			m.recordCallCompleted(ctx, sessionID, call, resolved.Source, actor, "", runSource, task.startedAt, task.result)
			task.finishedAt = storage.Now()
			ready <- index
			continue
		}
		assessment := prepared.RiskAssessment()
		riskText := string(assessment.Level)
		task.risk = riskText
		deps.AddToolUse(sessionID, call.Name)
		// 调用进度提示独立于需要交互确认时展示的准备结果。
		if !batchPreviewSent && deps.ShouldSendPreview(ctx, req.AssistantText) {
			deps.SendPreview(ctx, fmt.Sprintf("正在调用 %s：%s", call.Name, previewArguments(call.Arguments)))
		}
		var confirm toolshare.ConfirmResult
		if waitErr := await(func() {
			confirm, err = m.confirm(toolCtx, deps, actor, sessionID, call, resolved, assessment, prepared.ConfirmationDetails())
		}); waitErr != nil {
			return toolshare.RunResult{Err: waitErr}
		}
		confirmationExtra = joinAssistantText(confirmationExtra, confirm.Extra)
		if confirm.Stopped {
			return toolshare.RunResult{ConfirmationExtra: confirmationExtra, Stopped: true}
		}
		if !confirm.Allowed {
			if err == nil {
				err = toolshare.PolicyDeniedError("tool confirmation rejected")
			}
			task.result = executionError(call, err)
			if len(confirm.Message.Segments) > 0 {
				task.result.Message = confirm.Message
			}
			task.finishedAt = storage.Now()
			ready <- index
			continue
		}
		runToolCtx, done, err := deps.StartToolRequest(toolCtx, sessionID, call.Name)

		if err != nil {
			return toolshare.RunResult{Err: err}
		}
		runToolCtx = deps.PrepareToolContext(runToolCtx, &snapshot, call)
		// Validate prepared argument references without opening another write transaction.
		for _, id := range media.ArgumentMediaIDs(call.Arguments) {
			_, err = m.Media.Metadata(runToolCtx, id)
			if err != nil {
				break
			}
		}
		if err != nil {
			done()
			fail(call, err, fmt.Sprintf("tool call %s failed: retain media arguments: %v", call.Name, err))
			continue
		}
		workers.Go(func() {
			startedAt := time.Now()
			result := m.executePrepared(runToolCtx, call, resolved, prepared, func() {
				startedAt = time.Now()
				task.startedAt = startedAt
				m.recordCallStarted(runToolCtx, sessionID, call, resolved.Source, actor, riskText, runSource)
			})
			m.recordCallCompleted(runToolCtx, sessionID, call, resolved.Source, actor, riskText, runSource, startedAt, result)
			task.finishedAt = storage.Now()
			toolErr := runToolCtx.Err()
			done()
			if toolErr != nil && result.Error == nil {
				result.Error = toolErr
				result.Message.Segments = append(result.Message.Segments, llm.TextSegments(fmt.Sprintf("\ntool call %s canceled by user", call.Name))...)
			}
			task.result = result
			ready <- index
		})
	}
	for completed < len(tasks) {
		select {
		case index := <-ready:
			if err := complete(index); err != nil {
				return toolshare.RunResult{Err: err}
			}
		case <-ctx.Done():
			return toolshare.RunResult{Stopped: true, Err: ctx.Err()}
		}
	}
	err = req.Committer.Flush(ctx)

	return toolshare.RunResult{ConfirmationExtra: confirmationExtra, Err: err}
}

func sendBatchToolPreview(ctx context.Context, deps toolshare.RunnerDeps, req toolshare.RunRequest) bool {
	if len(req.Calls) <= 1 || !deps.ShouldSendPreview(ctx, req.AssistantText) {
		return false
	}
	lines := make([]string, 0, len(req.Calls))
	for _, call := range req.Calls {
		lines = append(lines, fmt.Sprintf("正在调用 %s：%s", call.Name, previewArguments(call.Arguments)))
	}
	deps.SendPreview(ctx, lines...)
	return true
}

func (m *Manager) confirm(ctx context.Context, deps toolshare.RunnerDeps, actor contextinfo.Actor, sessionID string, call llm.ToolCallRequest, resolved resolvedTool, assessment toolshare.RiskAssessment, detail []toolshare.ConfirmationDetailBlock) (toolshare.ConfirmResult, error) {
	message := llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID}
	if resolved.Source == toolshare.SourceKindELwisp {
		return toolshare.ConfirmResult{Allowed: true}, nil
	}
	info := resolved.Native.Definition
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	permission := m.Permission.CheckTool(actor, call.Name, assessment.Level, info.OwnerScoped, info.SuperadminOnly, toolshare.PolicyFromContext(ctx).AutoConfirmShell)
	if permission.Reason == "background_shell_auto_confirmed" || permission.Reason == "background_shell_rejected" {
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
			message.Segments = llm.TextSegments(fmt.Sprintf("tool call %s denied: requires superadmin role", call.Name))
			return toolshare.ConfirmResult{Message: message}, toolshare.PolicyDeniedError("tool requires superadmin")
		case "background_shell_rejected":
			message.Segments = llm.TextSegments("后台沙盒禁止执行 critical 风险 shell 命令。请改用相对路径，把文件限制在当前 sandbox 目录内，避免绝对路径、.. 逃逸和危险命令；若仍无法完成，请在最终报告中说明阻塞原因。")
			return toolshare.ConfirmResult{Message: message}, toolshare.PolicyDeniedError("background shell rejected")
		default:
			deps.AuditToolDenied(ctx, sessionID, call, assessment.Level, "tool_risk_above_allowed_level")
			message.Segments = llm.TextSegments(fmt.Sprintf("tool call %s denied: risk %s is above your allowed tool level", call.Name, assessment.Level))
			return toolshare.ConfirmResult{Message: message}, toolshare.PolicyDeniedError("tool risk above allowed level")
		}
	}
	if !permission.NeedsConfirmation {
		return toolshare.ConfirmResult{Allowed: true}, nil
	}
	return deps.ConfirmToolCall(ctx, sessionID, call, assessment, detail)
}

func toolMessage(name, id, content string) llm.LLMMessage {
	return llm.LLMMessage{Role: llm.RoleTool, Name: name, ToolCallID: id, Segments: llm.TextSegments(content)}
}

func previewArguments(args string) string {
	args = compactArguments(args)
	const maxPreviewRunes = 160
	if len([]rune(args)) <= maxPreviewRunes {
		return args
	}
	runes := []rune(args)
	return string(runes[:maxPreviewRunes]) + "..."
}

func compactArguments(args string) string {
	args = strings.TrimSpace(args)
	if args == "" {
		return "{}"
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(args)); err == nil {
		return compact.String()
	}
	return args
}

func joinAssistantText(first, second string) string {
	first = strings.TrimSpace(first)
	second = strings.TrimSpace(second)
	switch {
	case first == "":
		return second
	case second == "":
		return first
	default:
		return first + "\n\n" + second
	}
}
