package toolrun

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
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/llm"
	sandboxctx "elbot/internal/sandbox"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

type RunnerDeps interface {
	RefreshExecution(context.Context, *storage.Session) (context.Context, error)
	// Hook sources publish their own records; Runner consumes only the business return.
	PrepareToolCall(ctx context.Context, session *storage.Session, call llm.ToolCallRequest) (llm.ToolCallRequest, events.LogResult, error)
	ShouldSendPreview(ctx context.Context, session *storage.Session, call llm.ToolCallRequest, assistantText string) bool
	ConfirmToolCall(ctx context.Context, sessionID string, call llm.ToolCallRequest, assessment tool.RiskAssessment, detail string) (ConfirmResult, error)
	ConfirmBackgroundTool(ctx context.Context, sessionID string, call llm.ToolCallRequest, resolved ResolvedTool, assessment tool.RiskAssessment) (ConfirmResult, bool)
	// StartToolRequest returns its source registration result independently of
	// the business context, start time, cleanup function and original error.
	StartToolRequest(ctx context.Context, sessionID, toolName string) (context.Context, time.Time, func(), events.LogResult, error)
	PrepareToolContext(ctx context.Context, session *storage.Session, call llm.ToolCallRequest) context.Context
	// CompleteToolCall also exposes the declared Hook outcome separately from segments.
	CompleteToolCall(ctx context.Context, session *storage.Session, call llm.ToolCallRequest, risk string, segments []llm.MessageSegment, callErr error) ([]llm.MessageSegment, events.LogResult, error)
	// SendPreview takes one complete notice per tool call, including multiline
	// bodies. Pass batch notices separately so each receives one display prefix.
	SendPreview(ctx context.Context, text ...string)
	// SendOutputs returns the source logging result and body, including partial
	// delivery facts, separately from its original business error.
	SendOutputs(ctx context.Context, outputs []delivery.Output) (events.LogResult, error)
	// RecordToolCall saves the full business tool record and separately returns
	// the source persistence log result and original persistence error.
	RecordToolCall(ctx context.Context, sessionID string, call llm.ToolCallRequest, risk string, startedAt time.Time, result string, callErr error) (events.LogResult, error)
	AuditToolDenied(ctx context.Context, sessionID string, call llm.ToolCallRequest, risk tool.RiskLevel, reason string)
	RememberDiscoveryResult(ctx context.Context, session *storage.Session, result *tool.Result) (events.LogResult, error)
	AddToolUse(sessionID, toolName string)
	ToolResultMessage(sessionID string, message llm.LLMMessage) storage.Message
	ToolCallMessage(sessionID, content, rawText string, calls []llm.ToolCallRequest) storage.Message
}

type ConfirmResult struct {
	Allowed bool
	Extra   string
	Message llm.LLMMessage
	Stopped bool
}

// ToolCommitter is synchronous persistence owned by the consuming dialogue.
// Started precedes external effects; Result collects each outcome and Flush commits the batch.
// Each operation returns its source log result separately from the business error;
// phases without an actual write declare skipped instead of inventing a save.
type ToolCommitter interface {
	Begin(context.Context, *storage.Message) (events.LogResult, error)
	Prepared(context.Context, int, llm.ToolCallRequest) (events.LogResult, error)
	Started(context.Context, int, llm.ToolCallRequest) (events.LogResult, error)
	Result(context.Context, int, llm.ToolCallRequest, llm.LLMMessage, *storage.Message) (events.LogResult, error)
	Flush(context.Context) (events.LogResult, error)
}

type RunRequest struct {
	Committer        ToolCommitter
	Session          *storage.Session
	Calls            []llm.ToolCallRequest
	AssistantText    string
	AssistantRawText string
	CachedTools      []CachedTool
	Actor            contextinfo.Actor
}

type RunResult struct {
	Err               error
	Messages          []llm.LLMMessage
	PreparedCalls     []llm.ToolCallRequest
	ConfirmationExtra string
	Stopped           bool
}

// Runner belongs to the caller's orchestration; Manager supplies independent tool capabilities.
type Runner struct{ manager *Manager }

func NewRunner(manager *Manager) *Runner { return &Runner{manager: manager} }

func (r *Runner) Run(ctx context.Context, deps RunnerDeps, req RunRequest) RunResult {
	m := r.manager
	if req.Session.Mode == storage.SessionModeChat {
		return RunResult{}
	}
	sessionID := req.Session.ID
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
		logTool(ctx, record)
	}
	recordCall := func(ctx context.Context, call llm.ToolCallRequest, resolved ResolvedTool, actor contextinfo.Actor, risk string, runSource string, startedAt time.Time, result *ExecutionResult) {
		// Call entry declares invocation success, independently of the tool business
		// error. The tool supplies only its diagnostic log body.
		// runSource was fixed before entry; source separately identifies the
		// native/Elwisp implementation. Neither is inferred from diagnostics.
		if result.ExecutionStarted {
			result.LogResult.ResultStatus = events.ResultSucceeded
		}
		logResult := result.LogResult
		// Declaration order is display order; tools own their result summaries.
		content := struct {
			Tool       string `json:"tool"`
			ActorID    string `json:"actor_id"`
			Arguments  string `json:"arguments"`
			ElapsedMS  int64  `json:"elapsed_ms"`
			Result     string `json:"result"`
			Risk       string `json:"risk"`
			RunSource  string `json:"run_source"`
			Source     string `json:"source"`
			ToolCallID string `json:"tool_call_id"`
		}{
			Tool:       tool.TruncateLogContent(call.Name, 2048),
			ActorID:    tool.TruncateLogContent(actor.ID, 2048),
			Arguments:  tool.TruncateLogContent(call.Arguments, 256),
			ElapsedMS:  time.Since(startedAt).Milliseconds(),
			Result:     logResult.Content,
			Risk:       tool.TruncateLogContent(risk, 2048),
			RunSource:  tool.TruncateLogContent(runSource, 2048),
			Source:     tool.TruncateLogContent(string(resolved.Source), 2048),
			ToolCallID: tool.TruncateLogContent(call.ID, 2048),
		}
		data, _ := json.Marshal(content)
		record := newRecord(ctx, "tool_call", logResult)
		record.Category, record.Type, record.ContentLimit, record.Content = events.LogAudit, events.ContentJSON, 0, string(data)
		logTool(ctx, record)
	}
	ctx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	// Preparation and completion belong to this goroutine. Workers receive only
	// frozen call contexts and return one independent outcome through ready.
	tasks := make([]struct {
		ctx       context.Context
		risk      string
		startedAt time.Time
		result    ExecutionResult
		// ready carries the result of an actually started worker.
		ready <-chan ExecutionResult
	}, len(req.Calls))
	head := deps.ToolCallMessage(sessionID, req.AssistantText, req.AssistantRawText, req.Calls)
	logResult, err := req.Committer.Begin(ctx, &head)
	if logResult.ResultStatus == events.ResultFailed || logResult.ResultStatus == events.ResultCanceled {
		logTool(ctx, newRecord(ctx, "tool_batch_prepare_failed", logResult))
	}
	if err != nil {
		return RunResult{Err: err}
	}
	var confirmationExtra string
	batchPreviewSent := sendBatchToolPreview(ctx, deps, req)
	for index, original := range req.Calls {
		if ctx.Err() != nil {
			return RunResult{Stopped: true}
		}
		updated, err := deps.RefreshExecution(ctx, req.Session)
		if err != nil {
			return RunResult{Err: err}
		}
		ctx = updated
		actor := req.Actor
		if current, ok := contextinfo.ActorFromContext(ctx); ok {
			actor = current
		}
		// Session snapshots keep later handoffs and discovery writes out of workers.
		snapshot := *req.Session
		task := &tasks[index]
		task.ctx, task.startedAt = ctx, storage.Now()
		fail := func(call llm.ToolCallRequest, err error, content string) {
			task.result = ExecutionResult{Call: call, Message: toolMessage(call.Name, call.ID, content), Error: err}
		}
		call, _, err := deps.PrepareToolCall(ctx, req.Session, original)
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
		resolved := m.Resolve(ctx, call.Name, req.CachedTools, snapshot.Mode)
		runSource := "foreground"
		if sandbox, ok := sandboxctx.SandboxContextFromContext(ctx); ok && (sandbox.Background || sandbox.BackgroundKind != "") {
			runSource = ""
			switch sandbox.BackgroundKind {
			case sandboxctx.BackgroundKindCron, sandboxctx.BackgroundKindElnis:
				runSource = string(sandbox.BackgroundKind)
			}
		}
		if resolved.ResultStatus != events.ResultSucceeded {
			startedAt := time.Now()
			task.result = m.Execute(ctx, call, resolved, actor)
			recordCall(ctx, call, resolved, actor, "", runSource, startedAt, &task.result)
			continue
		}
		toolCtx := deps.PrepareToolContext(ctx, &snapshot, call)
		assessment, riskText, err := m.assessForRun(toolCtx, resolved, call)
		if err != nil {
			resultStatus := events.ResultFailed
			if errors.Is(err, context.Canceled) {
				resultStatus = events.ResultCanceled
			}
			record(ctx, call, "tool_prepare", events.LogResult{ResultStatus: resultStatus, Content: fmt.Sprintf("assess risk: %v", err)})
			fail(call, err, fmt.Sprintf("tool call %s failed: %v", call.Name, err))
			continue
		}
		task.risk = riskText
		deps.AddToolUse(sessionID, call.Name)
		prepared, err := m.PrepareCall(toolCtx, resolved, call)
		if err != nil {
			resultStatus := events.ResultFailed
			if errors.Is(err, context.Canceled) {
				resultStatus = events.ResultCanceled
			}
			record(ctx, call, "tool_prepare", events.LogResult{ResultStatus: resultStatus, Content: fmt.Sprintf("prepare_error=%v", err)})
			fail(call, err, fmt.Sprintf("tool call %s failed: %v", call.Name, err))
			continue
		}
		if !batchPreviewSent && deps.ShouldSendPreview(ctx, req.Session, call, req.AssistantText) {
			deps.SendPreview(ctx, fmt.Sprintf("正在调用 %s：%s", call.Name, previewArguments(call.Arguments)))
		}
		detail := ""
		if prepared != nil {
			detail = prepared.RiskDetail()
		}
		confirm, err := r.confirm(toolCtx, deps, actor, sessionID, call, resolved, assessment, detail)
		confirmationExtra = joinAssistantText(confirmationExtra, confirm.Extra)
		if confirm.Stopped {
			return RunResult{ConfirmationExtra: confirmationExtra, Stopped: true}
		}
		if !confirm.Allowed {
			if err == nil {
				err = tool.PolicyDeniedError("tool confirmation rejected")
			}
			message := confirm.Message
			if len(message.Segments) == 0 {
				message = toolMessage(call.Name, call.ID, fmt.Sprintf("tool call %s failed: %v", call.Name, err))
			}
			task.result = ExecutionResult{Call: call, Message: message, Error: err}
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
		{
			logResult, err := m.Media.RetainSessionToolArguments(runToolCtx, sessionID, call.Arguments)
			if logResult.ResultStatus == events.ResultFailed || logResult.ResultStatus == events.ResultCanceled {
				record(runToolCtx, call, "tool_prepare", logResult)
			}
			if err != nil {
				done()
				fail(call, err, fmt.Sprintf("tool call %s failed: retain media arguments: %v", call.Name, err))
				continue
			}
		}
		logResult, err = req.Committer.Started(runToolCtx, index, call)
		if logResult.ResultStatus == events.ResultFailed || logResult.ResultStatus == events.ResultCanceled {
			record(ctx, call, "tool_transcript_save", logResult)
		}
		if err != nil {
			done()
			fail(call, err, fmt.Sprintf("tool call %s was not executed: record start: %v", call.Name, err))
			continue
		}
		ready := make(chan ExecutionResult, 1)
		task.ready = ready
		workers.Add(1)
		go func() {
			defer workers.Done()
			startedAt := time.Now()
			result := m.ExecutePrepared(runToolCtx, call, resolved, actor, prepared)
			recordCall(runToolCtx, call, resolved, actor, riskText, runSource, startedAt, &result)
			toolErr := runToolCtx.Err()
			done()
			if toolErr != nil {
				result.Error = errors.Join(result.Error, toolErr)
				result.Message.Segments = append(result.Message.Segments, llm.TextSegments(fmt.Sprintf("\ntool call %s canceled by user", call.Name))...)
			}
			ready <- result
		}()
	}
	var resultErr error
	for index, task := range tasks {
		result := task.result
		if task.ready != nil {
			select {
			case result = <-task.ready:
			case <-ctx.Done():
				return RunResult{Stopped: true}
			}
		}
		if ctx.Err() != nil {
			return RunResult{Stopped: true}
		}
		updated, err := deps.RefreshExecution(ctx, req.Session)
		if err != nil {
			return RunResult{Err: err}
		}
		ctx = updated
		call := preparedCalls[index]
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
		if call.Name == "discover_tool" && result.Result != nil {
			logResult, stateErr := deps.RememberDiscoveryResult(ctx, req.Session, result.Result)
			if logResult.ResultStatus != events.ResultSkipped {
				record(ctx, call, "tool_discovery_save", logResult)
			}
			if stateErr != nil {
				result.Error = errors.Join(result.Error, stateErr)
				result.Message.Segments = append(result.Message.Segments, llm.TextSegments(fmt.Sprintf("\ntool call %s: tool state was not saved; completed actions were not rolled back: %v", call.Name, stateErr))...)
			}
		}
		logResult, _ := deps.RecordToolCall(task.ctx, sessionID, call, task.risk, task.startedAt, llm.SegmentsContentText(result.Message.Segments), result.Error)
		if logResult.ResultStatus != events.ResultSkipped {
			record(task.ctx, call, "tool_record_save", logResult)
		}
		messages[index] = result.Message
		stored := deps.ToolResultMessage(sessionID, result.Message)
		logResult, err = req.Committer.Result(ctx, index, call, result.Message, &stored)
		if logResult.ResultStatus != events.ResultSkipped {
			record(ctx, call, "tool_transcript_save", logResult)
		}
		resultErr = errors.Join(resultErr, err)
		if result.Error != nil {
			deps.SendPreview(ctx, fmt.Sprintf("%s 调用失败：%v", call.Name, result.Error))
		}
	}
	if resultErr != nil {
		return RunResult{Messages: messages, PreparedCalls: preparedCalls, ConfirmationExtra: confirmationExtra, Err: resultErr}
	}
	logResult, err = req.Committer.Flush(ctx)
	if logResult.ResultStatus != events.ResultSkipped {
		logTool(ctx, newRecord(ctx, "tool_transcript_save", logResult))
	}
	return RunResult{Messages: messages, PreparedCalls: preparedCalls, ConfirmationExtra: confirmationExtra, Err: err}
}

func sendBatchToolPreview(ctx context.Context, deps RunnerDeps, req RunRequest) bool {
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

func (r *Runner) confirm(ctx context.Context, deps RunnerDeps, actor contextinfo.Actor, sessionID string, call llm.ToolCallRequest, resolved ResolvedTool, assessment tool.RiskAssessment, detail string) (ConfirmResult, error) {
	m := r.manager
	message := llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID}
	if resolved.Source == SourceKindELwisp {
		return ConfirmResult{Allowed: true}, nil
	}
	policy := m.Policy
	info := resolved.Native.Info()
	if info.SuperadminOnly && actor.Role != contextinfo.RoleSuperadmin {
		deps.AuditToolDenied(ctx, sessionID, call, assessment.Level, "tool_requires_superadmin")
		message.Segments = llm.TextSegments(fmt.Sprintf("tool call %s denied: requires superadmin role", call.Name))
		return ConfirmResult{Message: message}, tool.PolicyDeniedError("tool requires superadmin")
	}
	if !policy.CanUseTool(actor, assessment.Level, info.OwnerScoped) {
		deps.AuditToolDenied(ctx, sessionID, call, assessment.Level, "tool_risk_above_allowed_level")
		message.Segments = llm.TextSegments(fmt.Sprintf("tool call %s denied: risk %s is above your allowed tool level", call.Name, assessment.Level))
		return ConfirmResult{Message: message}, tool.PolicyDeniedError("tool risk above allowed level")
	}
	if confirm, handled := deps.ConfirmBackgroundTool(ctx, sessionID, call, resolved, assessment); handled {
		return confirm, nil
	}
	if !policy.NeedsToolConfirmation(actor, assessment.Level) {
		return ConfirmResult{Allowed: true}, nil
	}
	return deps.ConfirmToolCall(ctx, sessionID, call, assessment, detail)
}

func (m *Manager) assessForRun(ctx context.Context, resolved ResolvedTool, call llm.ToolCallRequest) (tool.RiskAssessment, string, error) {
	assessment, err := m.AssessRisk(ctx, resolved, call.Arguments)
	return assessment, string(assessment.Level), err
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
