// 编排工具批次的准备、确认、执行和结果处理。
package execution

import toolshare "elbot/internal/tool/share"

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"elbot/internal/contextinfo"
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
	messages := make([]llm.LLMMessage, len(req.Calls))
	preparedCalls := append([]llm.ToolCallRequest(nil), req.Calls...)
	// Invocation and orchestration records are assembled at their own boundaries
	// in this runner. Publication receives only complete records.
	newRecord := func(ctx context.Context, eventName string, logResult events.LogResult) events.LogRecord {
		level := slog.LevelInfo
		if logResult.ResultStatus == events.ResultFailed {
			level = slog.LevelError
		}
		record := events.LogRecord{Category: events.LogRuntime, Level: level, EventName: eventName, Module: "toolrun", Type: events.ContentText, Content: logResult.Content, ContentLimit: 4096, Identity: events.LogIdentity{SessionID: &sessionID}, ResultStatus: logResult.ResultStatus}
		// Read the current operation's execution facts after any foreground handoff.
		// SessionID belongs to the supplied Session, including an explicit empty ID.
		facts, _ := contextinfo.ExecutionFromContext(ctx)
		if facts.RunID != "" {
			record.Identity.RunID = &facts.RunID
		}
		if facts.Attempt != "" {
			record.Identity.Attempt = &facts.Attempt
		}
		if facts.RequestID != "" {
			record.Identity.RequestID = &facts.RequestID
		}
		if facts.RootRequestID != "" {
			record.Identity.RootRequestID = &facts.RootRequestID
		}
		return record
	}
	record := func(ctx context.Context, call llm.ToolCallRequest, eventName string, logResult events.LogResult) {
		record := newRecord(ctx, eventName, logResult)
		record.Content = fmt.Sprintf("tool=%s tool_call_id=%s\n%s", call.Name, call.ID, logResult.Content)
		m.Log(ctx, record)
	}
	recordCall := func(ctx context.Context, call llm.ToolCallRequest, resolved resolvedTool, actor contextinfo.Actor, risk string, runSource string, startedAt time.Time, result *executionResult) {
		// The execution source declares the outcome; tools supply their diagnostic body.
		// runSource was fixed before entry; source separately identifies the
		// native/Elwisp implementation. Neither is inferred from diagnostics.
		if result.Result != nil && result.ExecutionStarted && resolved.Native != nil {
			result.Result.NeedsElyphRules = result.Result.NeedsElyphRules || resolved.Native.Info().NeedsElyphRules
		}
		if result.Result != nil && result.Result.DiscoveryText != nil {
			// The hook processes business details; the summary and common rules
			// are assembled once, after the hook, in model call order.
			result.Message.Segments = llm.TextSegments(result.Result.DiscoveryText.Details)
		}
		logResult := result.LogResult
		logResult.ResultStatus = result.ResultStatus
		// Keep the source-owned summary separate from the bounded actual result.
		content := struct {
			Tool       string `json:"tool"`
			ActorID    string `json:"actor_id"`
			ElapsedMS  int64  `json:"elapsed_ms"`
			Summary    string `json:"summary,omitempty"` // Source-owned summary, preserved without truncation.
			Result     string `json:"result"`
			Risk       string `json:"risk"`
			RunSource  string `json:"run_source"`
			Source     string `json:"source"`
			ToolCallID string `json:"tool_call_id"`
		}{
			Tool:       toolshare.TruncateLogContent(call.Name, 2048),
			ActorID:    toolshare.TruncateLogContent(actor.ID, 2048),
			ElapsedMS:  time.Since(startedAt).Milliseconds(),
			Summary:    logResult.Content,
			Result:     result.Result.LogContent(result.Error),
			Risk:       toolshare.TruncateLogContent(risk, 2048),
			RunSource:  toolshare.TruncateLogContent(runSource, 2048),
			Source:     toolshare.TruncateLogContent(string(resolved.Source), 2048),
			ToolCallID: toolshare.TruncateLogContent(call.ID, 2048),
		}
		data, _ := json.Marshal(content)
		record := newRecord(ctx, "tool_call_completed", logResult)
		record.Category, record.Type, record.ContentLimit, record.Content = events.LogAudit, events.ContentJSON, 0, string(data)
		m.Log(ctx, record)
	}
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
	logResult, err := req.Committer.Begin(ctx, &head)
	if logResult.ResultStatus == events.ResultFailed || logResult.ResultStatus == events.ResultCanceled {
		m.Log(ctx, newRecord(ctx, "tool_batch_prepare_failed", logResult))
	}
	if err != nil {
		return toolshare.RunResult{Err: err}
	}
	ready := make(chan int, len(req.Calls))
	completed := 0
	var resultErr error
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
		rawRecord := &storage.ToolCallRecord{SessionID: sessionID, ToolCallID: call.ID, ToolName: call.Name, ActorID: actor.ID, RiskLevel: task.risk, StartedAt: task.startedAt, FinishedAt: task.finishedAt, Success: result.Error == nil}
		if result.Error != nil {
			rawRecord.Error = result.Error.Error()
		}
		metadata := storage.SessionMetadata{}
		if result.Result != nil && len(result.Result.Outputs) > 0 {
			logResult, err := deps.SendOutputs(ctx, result.Result.Outputs)
			record(ctx, call, "tool_outputs", logResult)
			if err != nil {
				result.Error = errors.Join(result.Error, err)
				result.Message.Segments = append(result.Message.Segments, llm.TextSegments(fmt.Sprintf("\ntool call %s failed: output: %v", call.Name, err))...)
			}
		}
		if result.ExecutionStarted {
			segments, _, hookErr := deps.CompleteToolCall(ctx, req.Session, call, task.risk, result.Message.Segments, result.Error)
			if hookErr != nil {
				result.Error = errors.Join(result.Error, hookErr)
				result.Message.Segments = append(result.Message.Segments, llm.TextSegments(fmt.Sprintf("\ntool call %s failed: hook: %v", call.Name, hookErr))...)
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
		messages[index] = result.Message
		stored := deps.ToolResultMessage(sessionID, result.Message)
		if ruleCard != "" {
			fields, err := storage.DecodeSessionMetadata(stored.Metadata)
			if err != nil {
				return err
			}
			_ = fields.Set("elyph_rules_shown", true)
			stored.Metadata, _ = fields.Encode()
		}
		logResult, err = req.Committer.Result(ctx, index, call, result.Message, &storage.ToolResultCommit{Result: &stored, Record: rawRecord, Metadata: metadata})
		if logResult.ResultStatus != events.ResultSkipped {
			record(ctx, call, "tool_transcript_save", logResult)
		}
		if err != nil {
			return err
		}
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
		if result.Error != nil {
			deps.SendPreview(ctx, fmt.Sprintf("%s 调用失败：%v", call.Name, result.Error))
		}
		return nil
	}
	// Blocking preparation receives a detached snapshot; completed tools can be
	// persisted while a later Hook or confirmation is waiting.
	await := func(run func()) {
		done := make(chan struct{})
		go func() { defer close(done); run() }()
		for {
			select {
			case <-done:
				return
			case index := <-ready:
				if err := complete(index); err != nil {
					resultErr = errors.Join(resultErr, err)
					cancel()
				}
			}
		}
	}
	var confirmationExtra string
	batchPreviewSent := sendBatchToolPreview(ctx, deps, req)
	for index, original := range req.Calls {
		if ctx.Err() != nil {
			return toolshare.RunResult{Stopped: true, Err: resultErr}
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
			task.result = executionResult{Call: call, Message: toolMessage(call.Name, call.ID, content), Error: err}
			task.finishedAt = storage.Now()
			ready <- index
		}
		var call llm.ToolCallRequest
		prepareCtx := ctx
		await(func() { call, _, err = deps.PrepareToolCall(prepareCtx, &snapshot, original) })
		if err != nil {
			fail(original, err, fmt.Sprintf("tool call %s failed: hook: %v", original.Name, err))
			continue
		}
		preparedCalls[index] = call
		logResult, err := req.Committer.Prepared(ctx, index, call)
		if logResult.ResultStatus == events.ResultFailed || logResult.ResultStatus == events.ResultCanceled {
			record(ctx, call, "tool_transcript_save", logResult)
		}
		if err != nil {
			preparedCalls[index] = original
			fail(original, err, fmt.Sprintf("tool call %s was not executed: prepare transcript: %v", original.Name, err))
			continue
		}
		resolved := m.resolve(ctx, call.Name, req.DiscoveredTools, snapshot.Mode)
		runSource := "foreground"
		if backgroundKind, ok := contextinfo.BackgroundKindFromContext(ctx); ok {
			runSource = ""
			switch backgroundKind {
			case contextinfo.BackgroundKindCron, contextinfo.BackgroundKindElnis:
				runSource = string(backgroundKind)
			}
		}
		if resolved.ResultStatus != events.ResultSucceeded {
			startedAt := time.Now()
			task.result = m.executePrepared(ctx, call, resolved, nil, nil)
			recordCall(ctx, call, resolved, actor, "", runSource, startedAt, &task.result)
			task.finishedAt = storage.Now()
			ready <- index
			continue
		}
		toolCtx := deps.PrepareToolContext(ctx, &snapshot, call)
		var prepared *toolshare.PreparedCall
		await(func() { prepared, err = m.prepareCall(toolCtx, resolved, call) })
		if err != nil {
			resultStatus := events.ResultFailed
			if errors.Is(err, context.Canceled) {
				resultStatus = events.ResultCanceled
			}
			record(ctx, call, "tool_prepare", events.LogResult{ResultStatus: resultStatus, Content: fmt.Sprintf("prepare_error=%v", err)})
			fail(call, err, fmt.Sprintf("tool call %s failed: %v", call.Name, err))
			continue
		}
		assessment := prepared.RiskAssessment()
		riskText := string(assessment.Level)
		task.risk = riskText
		deps.AddToolUse(sessionID, call.Name)
		// 调用进度提示独立于需要交互确认时展示的准备结果。
		if !batchPreviewSent && deps.ShouldSendPreview(ctx, req.Session, call, req.AssistantText) {
			deps.SendPreview(ctx, fmt.Sprintf("正在调用 %s：%s", call.Name, previewArguments(call.Arguments)))
		}
		var confirm toolshare.ConfirmResult
		await(func() {
			confirm, err = m.confirm(toolCtx, deps, actor, sessionID, call, resolved, assessment, prepared.Preview())
		})
		confirmationExtra = joinAssistantText(confirmationExtra, confirm.Extra)
		if resultErr != nil {
			return toolshare.RunResult{Err: resultErr}
		}
		if confirm.Stopped {
			return toolshare.RunResult{ConfirmationExtra: confirmationExtra, Stopped: true}
		}
		if !confirm.Allowed {
			if err == nil {
				err = toolshare.PolicyDeniedError("tool confirmation rejected")
			}
			message := confirm.Message
			if len(message.Segments) == 0 {
				message = toolMessage(call.Name, call.ID, fmt.Sprintf("tool call %s failed: %v", call.Name, err))
			}
			task.result = executionResult{Call: call, Message: message, Error: err}
			task.finishedAt = storage.Now()
			ready <- index
			continue
		}
		runToolCtx, _, done, logResult, err := deps.StartToolRequest(toolCtx, sessionID, call.Name)
		if logResult.ResultStatus == events.ResultFailed || logResult.ResultStatus == events.ResultCanceled {
			record(ctx, call, "tool_prepare", logResult)
		}
		if err != nil {
			fail(call, err, fmt.Sprintf("tool call %s failed: %v", call.Name, err))
			continue
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
		workers.Add(1)
		go func() {
			defer workers.Done()
			startedAt := time.Now()
			result := m.executePrepared(runToolCtx, call, resolved, prepared, func() {
				startedAt = time.Now()
				task.startedAt = startedAt
				content, _ := json.Marshal(map[string]string{"tool": call.Name, "tool_call_id": call.ID, "actor_id": actor.ID, "arguments": toolshare.TruncateLogContent(call.Arguments, 512), "risk": riskText, "run_source": runSource, "source": string(resolved.Source)})
				record := newRecord(runToolCtx, "tool_call_prepared", events.LogResult{ResultStatus: events.ResultSucceeded})
				record.Category, record.Type, record.ContentLimit, record.Content = events.LogAudit, events.ContentJSON, 0, string(content)
				m.Log(runToolCtx, record)
			})
			recordCall(runToolCtx, call, resolved, actor, riskText, runSource, startedAt, &result)
			task.finishedAt = storage.Now()
			toolErr := runToolCtx.Err()
			done()
			if toolErr != nil {
				result.Error = errors.Join(result.Error, toolErr)
				result.Message.Segments = append(result.Message.Segments, llm.TextSegments(fmt.Sprintf("\ntool call %s canceled by user", call.Name))...)
			}
			task.result = result
			ready <- index
		}()
	}
	for completed < len(tasks) {
		select {
		case index := <-ready:
			if err := complete(index); err != nil {
				resultErr = errors.Join(resultErr, err)
				cancel()
			}
		case <-ctx.Done():
			return toolshare.RunResult{Stopped: true, Err: resultErr}
		}
	}
	if resultErr != nil {
		return toolshare.RunResult{Messages: messages, PreparedCalls: preparedCalls, ConfirmationExtra: confirmationExtra, Err: resultErr}
	}
	logResult, err = req.Committer.Flush(ctx)
	if logResult.ResultStatus != events.ResultSkipped {
		m.Log(ctx, newRecord(ctx, "tool_transcript_save", logResult))
	}
	return toolshare.RunResult{Messages: messages, PreparedCalls: preparedCalls, ConfirmationExtra: confirmationExtra, Err: err}
}

func sendBatchToolPreview(ctx context.Context, deps toolshare.RunnerDeps, req toolshare.RunRequest) bool {
	if len(req.Calls) <= 1 || !deps.ShouldSendPreview(ctx, req.Session, llm.ToolCallRequest{}, req.AssistantText) {
		return false
	}
	lines := make([]string, 0, len(req.Calls))
	for _, call := range req.Calls {
		lines = append(lines, fmt.Sprintf("正在调用 %s：%s", call.Name, previewArguments(call.Arguments)))
	}
	deps.SendPreview(ctx, lines...)
	return true
}

func (m *Manager) confirm(ctx context.Context, deps toolshare.RunnerDeps, actor contextinfo.Actor, sessionID string, call llm.ToolCallRequest, resolved resolvedTool, assessment toolshare.RiskAssessment, detail string) (toolshare.ConfirmResult, error) {
	message := llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID}
	if resolved.Source == toolshare.SourceKindELwisp {
		return toolshare.ConfirmResult{Allowed: true}, nil
	}
	info := resolved.Native.Info()
	kind, _ := contextinfo.BackgroundKindFromContext(ctx)
	backgroundKind := string(kind)
	permission := m.Permission.CheckTool(actor, call.Name, assessment.Level, info.OwnerScoped, info.SuperadminOnly, backgroundKind)
	if permission.Reason == "background_shell_auto_confirmed" || permission.Reason == "background_shell_rejected" {
		scope, _ := workspace.PathScopeFromContext(ctx)
		content := map[string]string{"kind": backgroundKind, "tool": call.Name, "risk": string(assessment.Level), "sandbox_dir": scope.Dir, "arguments": call.Arguments}
		for key, value := range content {
			content[key] = toolshare.TruncateLogContent(value, 2048)
		}
		data, _ := json.Marshal(content)
		resultStatus := events.ResultSucceeded
		if !permission.Allowed {
			resultStatus = events.ResultRejected
		}
		record := events.LogRecord{Category: events.LogAudit, Level: slog.LevelInfo, EventName: permission.Reason, Module: "tool", Type: events.ContentJSON, Content: string(data), Identity: events.LogIdentity{SessionID: &sessionID}, ResultStatus: resultStatus}
		facts, _ := contextinfo.ExecutionFromContext(ctx)
		if facts.RunID != "" {
			record.Identity.RunID = &facts.RunID
		}
		if facts.Attempt != "" {
			record.Identity.Attempt = &facts.Attempt
		}
		if facts.RequestID != "" {
			record.Identity.RequestID = &facts.RequestID
		}
		if facts.RootRequestID != "" {
			record.Identity.RootRequestID = &facts.RootRequestID
		}
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
