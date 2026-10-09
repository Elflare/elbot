package toolrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

type RunnerDeps interface {
	RefreshExecution(context.Context, *storage.Session) (context.Context, error)
	PrepareToolCall(ctx context.Context, session *storage.Session, call llm.ToolCallRequest) (llm.ToolCallRequest, error)
	ShouldSendPreview(ctx context.Context, session *storage.Session, call llm.ToolCallRequest, assistantText string) bool
	ConfirmToolCall(ctx context.Context, sessionID string, call llm.ToolCallRequest, assessment tool.RiskAssessment, detail string) (ConfirmResult, error)
	ConfirmBackgroundTool(ctx context.Context, sessionID string, call llm.ToolCallRequest, resolved ResolvedTool, assessment tool.RiskAssessment) (ConfirmResult, bool)
	StartToolRequest(ctx context.Context, sessionID, toolName string) (context.Context, time.Time, func(), error)
	PrepareToolContext(ctx context.Context, session *storage.Session, call llm.ToolCallRequest) context.Context
	CompleteToolCall(ctx context.Context, session *storage.Session, call llm.ToolCallRequest, risk string, segments []llm.MessageSegment, callErr error) ([]llm.MessageSegment, error)
	SendPreview(ctx context.Context, text string)
	SendOutputs(ctx context.Context, outputs []delivery.Output) error
	// RecordToolCall saves the full tool record and returns its persistence error.
	RecordToolCall(ctx context.Context, sessionID string, call llm.ToolCallRequest, risk string, startedAt time.Time, result string, callErr error) error
	AuditToolDenied(ctx context.Context, sessionID string, call llm.ToolCallRequest, risk tool.RiskLevel, reason string)
	RememberDiscoveryResult(ctx context.Context, session *storage.Session, result *tool.Result) error
	AddToolUse(sessionID, toolName string)
	ToolResultMessage(sessionID string, message llm.LLMMessage) storage.Message
	ToolCallMessage(sessionID, content, rawText string, calls []llm.ToolCallRequest) storage.Message
	PersistedToolMessage(message llm.LLMMessage) llm.LLMMessage
}

type ConfirmResult struct {
	Allowed bool
	Extra   string
	Message llm.LLMMessage
	Stopped bool
}

// ToolCommitter is synchronous persistence owned by the consuming dialogue.
// Its Started boundary precedes external tool effects; Result precedes the next call.
type ToolCommitter interface {
	Begin(context.Context, *storage.Message) error
	Prepared(context.Context, int, llm.ToolCallRequest) error
	Started(context.Context, int, llm.ToolCallRequest) error
	Result(context.Context, int, llm.ToolCallRequest, llm.LLMMessage, *storage.Message) error
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

func (m *Manager) Run(ctx context.Context, deps RunnerDeps, req RunRequest) RunResult {
	if req.Session == nil || req.Session.Mode == storage.SessionModeChat || deps == nil {
		return RunResult{}
	}
	if req.Committer == nil {
		return RunResult{Err: fmt.Errorf("tool transcript committer is required")}
	}
	sessionID := req.Session.ID
	messages := make([]llm.LLMMessage, 0, len(req.Calls))
	preparedCalls := make([]llm.ToolCallRequest, 0, len(req.Calls))
	head := deps.ToolCallMessage(sessionID, req.AssistantText, req.AssistantRawText, req.Calls)
	if err := req.Committer.Begin(ctx, &head); err != nil {
		record := events.LogRecord{Category: events.LogRuntime, Level: slog.LevelError, EventName: "tool_batch_prepare_failed", Module: "toolrun", Type: events.ContentText, Content: fmt.Sprintf("persistence_error=%v", err), ContentLimit: 4096, ResultStatus: events.ResultFailed}
		record.Identity.SessionID = &sessionID
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
		_ = events.EmitLog(context.WithoutCancel(ctx), record)
		return RunResult{Err: err}
	}
	persistResult := func(index int, call llm.ToolCallRequest) error {
		message := messages[len(messages)-1]
		stored := deps.ToolResultMessage(sessionID, deps.PersistedToolMessage(message))
		return req.Committer.Result(ctx, index, call, message, &stored)
	}
	persistenceFailure := func(err error) *RunResult {
		return &RunResult{Messages: messages, PreparedCalls: preparedCalls, Err: err}
	}
	var confirmationExtra string
	batchPreviewSent := sendBatchToolPreview(ctx, deps, req)
	for index, original := range req.Calls {
		result := func() *RunResult {
			if ctx.Err() != nil {
				return &RunResult{Messages: messages, PreparedCalls: preparedCalls, Stopped: true}
			}
			updated, err := deps.RefreshExecution(ctx, req.Session)
			if err != nil {
				return &RunResult{Messages: messages, PreparedCalls: preparedCalls, Stopped: true}
			}
			ctx = updated
			if actor, ok := contextinfo.ActorFromContext(ctx); ok {
				req.Actor = actor
			}
			startedAt := storage.Now()

			content := map[string]any{"tool": original.Name, "tool_call_id": original.ID, "actor_id": req.Actor.ID, "arguments": fmt.Sprintf("%.2048s", original.Arguments)}
			record := events.LogRecord{Category: events.LogAudit, Level: slog.LevelInfo, EventName: "tool_call", Module: "toolrun", Type: events.ContentJSON, ResultStatus: events.ResultSucceeded}
			record.Identity.SessionID = &sessionID
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
			defer func() {
				content["elapsed_ms"] = time.Since(startedAt).Milliseconds()
				data, _ := json.Marshal(content)
				record.Content = string(data)
				_ = events.EmitLog(context.WithoutCancel(ctx), record)
				record.Category, record.Type = events.LogRuntime, events.ContentText
				record.ContentLimit = 4096
				record.Content = string(data)
				_ = events.EmitLog(context.WithoutCancel(ctx), record)
			}()

			call, err := deps.PrepareToolCall(ctx, req.Session, original)
			if err != nil {
				content["prepare_error"] = fmt.Sprintf("%.2048s", err.Error())
				record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
				if errors.Is(err, context.Canceled) {
					record.ResultStatus, record.Level = events.ResultCanceled, slog.LevelInfo
				}
				preparedCalls = append(preparedCalls, original)
				message := toolMessage(original.Name, original.ID, fmt.Sprintf("tool call %s failed: hook: %v", original.Name, err))
				messages = append(messages, message)
				if err := persistResult(index, original); err != nil {
					content["persistence_error"] = fmt.Sprintf("%.2048s", err.Error())
					record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
					return persistenceFailure(err)
				}
				return nil
			}
			content["tool"], content["tool_call_id"], content["arguments"] = call.Name, call.ID, fmt.Sprintf("%.2048s", call.Arguments)
			preparedCalls = append(preparedCalls, call)
			if err := req.Committer.Prepared(ctx, index, call); err != nil {
				content["persistence_error"] = fmt.Sprintf("%.2048s", err.Error())
				record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
				return persistenceFailure(err)
			}
			resolved := m.Resolve(ctx, call.Name, req.CachedTools, req.Session.Mode)
			content["source"] = resolved.Source
			if resolved.ResultStatus != events.ResultSucceeded {
				err := resolved.Error
				content["resolution_error"] = fmt.Sprintf("%.2048s", err.Error())
				record.ResultStatus = resolved.ResultStatus
				record.Level = slog.LevelError
				if record.ResultStatus == events.ResultRejected {
					record.Level = slog.LevelWarn
				}
				message := toolMessage(call.Name, call.ID, fmt.Sprintf("tool call %s failed: %v", call.Name, err))
				if recordErr := deps.RecordToolCall(ctx, sessionID, call, "", startedAt, llm.SegmentsContentText(message.Segments), err); recordErr != nil {
					content["record_error"] = fmt.Sprintf("%.2048s", recordErr.Error())
					record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
				}
				messages = append(messages, message)
				if err := persistResult(index, call); err != nil {
					content["persistence_error"] = fmt.Sprintf("%.2048s", err.Error())
					record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
					return persistenceFailure(err)
				}
				return nil
			}
			toolCtx := deps.PrepareToolContext(ctx, req.Session, call)
			assessment, riskText := m.assessForRun(toolCtx, resolved, call)
			content["risk"] = riskText
			deps.AddToolUse(sessionID, call.Name)
			if err := m.PreflightConfirmation(toolCtx, resolved, call); err != nil {
				content["prepare_error"] = fmt.Sprintf("%.2048s", err.Error())
				record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
				if errors.Is(err, context.Canceled) {
					record.ResultStatus, record.Level = events.ResultCanceled, slog.LevelInfo
				}
				message := toolMessage(call.Name, call.ID, fmt.Sprintf("tool call %s failed: %v", call.Name, err))
				resultText := llm.SegmentsContentText(message.Segments)
				if recordErr := deps.RecordToolCall(ctx, sessionID, call, riskText, startedAt, resultText, err); recordErr != nil {
					content["record_error"] = fmt.Sprintf("%.2048s", recordErr.Error())
					record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
				}
				messages = append(messages, message)
				if err := persistResult(index, call); err != nil {
					content["persistence_error"] = fmt.Sprintf("%.2048s", err.Error())
					record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
					return persistenceFailure(err)
				}
				return nil
			}
			if !batchPreviewSent && deps.ShouldSendPreview(ctx, req.Session, call, req.AssistantText) {
				deps.SendPreview(ctx, fmt.Sprintf("正在调用 %s：%s", call.Name, previewArguments(call.Arguments)))
			}
			confirm, err := m.confirm(toolCtx, deps, req.Actor, sessionID, call, resolved, assessment)
			if !confirm.Allowed {
				record.ResultStatus, record.Level = events.ResultRejected, slog.LevelWarn
				content["confirmation"] = "rejected"
				if err != nil {
					content["confirmation_error"] = fmt.Sprintf("%.2048s", err.Error())
				}
				if confirm.Stopped {
					record.ResultStatus, record.Level = events.ResultCanceled, slog.LevelInfo
				}
				if err != nil {
					switch {
					case errors.Is(err, context.Canceled):
						record.ResultStatus, record.Level = events.ResultCanceled, slog.LevelInfo
					case errors.As(err, new(tool.PolicyDeniedError)):
						record.ResultStatus, record.Level = events.ResultRejected, slog.LevelWarn
					default:
						record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
					}
				}
				content["confirmation"] = record.ResultStatus.String()
				if confirm.Stopped {
					return &RunResult{Messages: messages, PreparedCalls: preparedCalls, ConfirmationExtra: confirmationExtra, Stopped: true}
				}
				message := confirm.Message
				if len(message.Segments) == 0 {
					message = toolMessage(call.Name, call.ID, fmt.Sprintf("tool call %s failed: %v", call.Name, err))
				}
				messageText := llm.SegmentsContentText(message.Segments)
				if err == nil {
					err = tool.PolicyDeniedError("tool confirmation rejected")
				}
				if recordErr := deps.RecordToolCall(ctx, sessionID, call, riskText, startedAt, messageText, err); recordErr != nil {
					content["record_error"] = fmt.Sprintf("%.2048s", recordErr.Error())
					record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
				}
				messages = append(messages, message)
				confirmationExtra = joinAssistantText(confirmationExtra, confirm.Extra)
				if err := persistResult(index, call); err != nil {
					content["persistence_error"] = fmt.Sprintf("%.2048s", err.Error())
					record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
					return persistenceFailure(err)
				}
				return nil
			}
			confirmationExtra = joinAssistantText(confirmationExtra, confirm.Extra)
			runToolCtx, _, done, err := deps.StartToolRequest(toolCtx, sessionID, call.Name)
			if err != nil {
				record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
				content["request_error"] = fmt.Sprintf("%.2048s", err.Error())
				if errors.Is(err, context.Canceled) {
					record.ResultStatus, record.Level = events.ResultCanceled, slog.LevelInfo
				}
				resultText := fmt.Sprintf("tool call %s failed: %v", call.Name, err)
				message := toolMessage(call.Name, call.ID, resultText)
				if recordErr := deps.RecordToolCall(ctx, sessionID, call, riskText, startedAt, resultText, err); recordErr != nil {
					content["record_error"] = fmt.Sprintf("%.2048s", recordErr.Error())
					record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
				}
				messages = append(messages, message)
				if err := persistResult(index, call); err != nil {
					content["persistence_error"] = fmt.Sprintf("%.2048s", err.Error())
					record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
					return persistenceFailure(err)
				}
				return nil
			}
			runToolCtx = deps.PrepareToolContext(runToolCtx, req.Session, call)
			facts, _ = contextinfo.ExecutionFromContext(runToolCtx)
			if facts.RequestID != "" {
				record.Identity.RequestID = &facts.RequestID
			}
			if m.Media != nil {
				if err := m.Media.RetainSessionToolArguments(runToolCtx, sessionID, call.Arguments); err != nil {
					content["media_error"] = fmt.Sprintf("%.2048s", err.Error())
					record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
					done()
					message := toolMessage(call.Name, call.ID, fmt.Sprintf("tool call %s failed: retain media arguments: %v", call.Name, err))
					resultText := llm.SegmentsContentText(message.Segments)
					if recordErr := deps.RecordToolCall(ctx, sessionID, call, riskText, startedAt, resultText, err); recordErr != nil {
						content["record_error"] = fmt.Sprintf("%.2048s", recordErr.Error())
						record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
					}
					messages = append(messages, message)
					if err := persistResult(index, call); err != nil {
						content["persistence_error"] = fmt.Sprintf("%.2048s", err.Error())
						record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
						return persistenceFailure(err)
					}
					return nil
				}
			}
			if err := req.Committer.Started(runToolCtx, index, call); err != nil {
				content["persistence_error"] = fmt.Sprintf("%.2048s", err.Error())
				record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
				done()
				return persistenceFailure(err)
			}
			result := m.Execute(runToolCtx, call, resolved, req.Actor)
			record.ResultStatus = result.ResultStatus
			content["execution_started"] = result.ExecutionStarted
			content["result"] = fmt.Sprintf("%.2048s", llm.SegmentsContentText(result.Message.Segments))
			if result.Error != nil {
				content["call_error"] = fmt.Sprintf("%.2048s", result.Error.Error())
			}
			if record.ResultStatus == events.ResultFailed {
				record.Level = slog.LevelError
			}
			if record.ResultStatus == events.ResultRejected {
				record.Level = slog.LevelWarn
			}
			toolErr := runToolCtx.Err()
			done()
			if err := ctx.Err(); err != nil {
				content["request_error"] = fmt.Sprintf("%.2048s", err.Error())
				if record.ResultStatus != events.ResultFailed {
					record.ResultStatus, record.Level = events.ResultCanceled, slog.LevelInfo
					if !errors.Is(err, context.Canceled) {
						record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
					}
				}
				return &RunResult{Messages: messages, PreparedCalls: preparedCalls, Stopped: true}
			}
			if err := toolErr; err != nil {

				content["request_error"] = fmt.Sprintf("%.2048s", err.Error())
				if result.Error == nil {
					record.ResultStatus, record.Level = events.ResultCanceled, slog.LevelInfo
					if !errors.Is(err, context.Canceled) {
						record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
					}
				}
				result.Error = errors.Join(result.Error, err)
				result.Message = toolMessage(call.Name, call.ID, fmt.Sprintf("tool call %s canceled by user", call.Name))
			}
			if result.Result != nil && len(result.Result.Outputs) > 0 {
				if err := deps.SendOutputs(ctx, result.Result.Outputs); err != nil {
					content["output_error"] = fmt.Sprintf("%.2048s", err.Error())
					record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
					result.Error = errors.Join(result.Error, err)
					result.Message.Segments = llm.TextSegments(fmt.Sprintf("tool call %s failed: output: %v", call.Name, err))
				}
			}
			completedSegments, hookErr := deps.CompleteToolCall(ctx, req.Session, call, riskText, result.Message.Segments, result.Error)
			if hookErr != nil {
				content["hook_error"] = fmt.Sprintf("%.2048s", hookErr.Error())
				record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
				result.Error = errors.Join(result.Error, hookErr)
				result.Message.Segments = llm.TextSegments(fmt.Sprintf("tool call %s failed: hook: %v", call.Name, hookErr))
			} else {
				result.Message.Segments = completedSegments
			}
			if call.Name == "discover_tool" && result.Result != nil {
				if stateErr := deps.RememberDiscoveryResult(ctx, req.Session, result.Result); stateErr != nil {
					content["state_error"] = fmt.Sprintf("%.2048s", stateErr.Error())
					record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
					result.Error = errors.Join(result.Error, stateErr)
					result.Message.Segments = llm.TextSegments(fmt.Sprintf("tool call %s: tool state was not saved; completed actions were not rolled back: %v", call.Name, result.Error))
				}
			}
			resultText := llm.SegmentsContentText(result.Message.Segments)
			content["final_result"] = fmt.Sprintf("%.2048s", resultText)
			if recordErr := deps.RecordToolCall(ctx, sessionID, call, riskText, startedAt, resultText, result.Error); recordErr != nil {
				content["record_error"] = fmt.Sprintf("%.2048s", recordErr.Error())
				record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
			}
			messages = append(messages, result.Message)
			if err := persistResult(index, call); err != nil {
				content["persistence_error"] = fmt.Sprintf("%.2048s", err.Error())
				record.ResultStatus, record.Level = events.ResultFailed, slog.LevelError
				return persistenceFailure(err)
			}
			if result.Error != nil {
				deps.SendPreview(ctx, fmt.Sprintf("%s 调用失败：%v", call.Name, result.Error))
			}
			return nil
		}()
		if result != nil {
			return *result
		}
	}
	return RunResult{Messages: messages, PreparedCalls: preparedCalls, ConfirmationExtra: confirmationExtra}
}

func sendBatchToolPreview(ctx context.Context, deps RunnerDeps, req RunRequest) bool {
	if len(req.Calls) <= 1 || !deps.ShouldSendPreview(ctx, req.Session, llm.ToolCallRequest{}, req.AssistantText) {
		return false
	}
	lines := make([]string, 0, len(req.Calls))
	for _, call := range req.Calls {
		lines = append(lines, fmt.Sprintf("正在调用 %s：%s", call.Name, previewArguments(call.Arguments)))
	}
	deps.SendPreview(ctx, strings.Join(lines, "\n"))
	return true
}

func (m *Manager) confirm(ctx context.Context, deps RunnerDeps, actor contextinfo.Actor, sessionID string, call llm.ToolCallRequest, resolved ResolvedTool, assessment tool.RiskAssessment) (ConfirmResult, error) {
	message := llm.LLMMessage{Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID}
	if resolved.ResultStatus != events.ResultSucceeded {
		message.Segments = llm.TextSegments(fmt.Sprintf("tool call %s failed: %v", call.Name, resolved.Error))
		return ConfirmResult{Message: message}, resolved.Error
	}
	if resolved.Source == SourceKindELwisp {
		return ConfirmResult{Allowed: true}, nil
	}
	policy := policyForManager(ctx, m.Policy)
	if resolved.Native == nil {
		message.Segments = llm.TextSegments(fmt.Sprintf("tool call %s failed: native tool unavailable", call.Name))
		return ConfirmResult{Message: message}, fmt.Errorf("native tool unavailable")
	}
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
	return deps.ConfirmToolCall(ctx, sessionID, call, assessment, m.RiskDetail(ctx, resolved, call))
}

func (m *Manager) assessForRun(ctx context.Context, resolved ResolvedTool, call llm.ToolCallRequest) (tool.RiskAssessment, string) {
	assessment, err := m.AssessRisk(ctx, resolved, call.Arguments)
	if err != nil || assessment.Level == "" {
		return tool.RiskAssessment{Level: tool.RiskLow}, "unknown"
	}
	return assessment, string(assessment.Level)
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
