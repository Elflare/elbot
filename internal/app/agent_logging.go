package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	agentevents "elbot/internal/agent/events"
)

// writeAgentLog uses the published identity snapshot, not the consumer's context.
func writeAgentLog(ctx context.Context, logger *slog.Logger, meta agentevents.EventMeta, level slog.Level, message string, attrs ...any) error {
	fields := make([]any, 0, 10+len(attrs))
	for _, field := range []struct{ key, value string }{
		{"session_id", meta.SessionID},
		{"run_id", meta.RunID},
		{"attempt", meta.Attempt},
		{"request_id", meta.RequestID},
		{"root_request_id", meta.RootRequestID},
	} {
		if field.value != "" {
			fields = append(fields, field.key, field.value)
		}
	}
	return writeLog(ctx, logger, meta.At, level, message, append(fields, attrs...)...)
}

type agentLogger struct{ runtime, audit *slog.Logger }

func (l agentLogger) userInput(ctx context.Context, e agentevents.UserInputReceivedEvent) error {
	return writeAgentLog(ctx, l.runtime, e.EventMeta, slog.LevelInfo, "user input", "event", "user_message", "text", logPreview(e.Text, 120))
}
func (l agentLogger) persistence(ctx context.Context, e agentevents.PersistenceFailedEvent) error {
	return l.auditEvent(ctx, e.EventMeta, "persistence_error", "operation", e.Operation, "error", e.Err.Error())
}
func (l agentLogger) timeoutOutput(ctx context.Context, e agentevents.TurnTimedOutEvent) error {
	return writeAgentLog(ctx, l.runtime, e.EventMeta, slog.LevelWarn, "turn response timeout", "error", e.Err.Error())
}
func (l agentLogger) timeoutAudit(ctx context.Context, e agentevents.TurnTimedOutEvent) error {
	return l.auditEvent(ctx, e.EventMeta, "turn_response_timeout", "error", e.Err.Error())
}

func logPreview(text string, limit int) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit]) + "..."
	}
	return text
}
func logArguments(args string) string {
	args = strings.TrimSpace(args)
	if args == "" {
		args = "{}"
	}
	var compact bytes.Buffer
	if json.Compact(&compact, []byte(args)) == nil {
		args = compact.String()
	}
	return logPreview(args, 160)
}
func (l agentLogger) auditEvent(ctx context.Context, meta agentevents.EventMeta, event string, attrs ...any) error {
	return writeAgentLog(ctx, l.audit, meta, slog.LevelInfo, "audit event", append([]any{"event", event}, attrs...)...)
}
func (l agentLogger) modelOutput(ctx context.Context, e agentevents.ModelCallCompletedEvent) error {
	if !e.OutputReady {
		return nil
	}
	return writeAgentLog(ctx, l.runtime, e.EventMeta, slog.LevelInfo, "llm output", "event", "assistant_message", "provider", e.Provider, "model", e.Model, "elapsed_ms", e.ElapsedMS, "text", logPreview(e.Text, 120), "raw_text", logPreview(e.SourceText, 120), "tool_call_count", e.ToolCallCount)
}
func (l agentLogger) modelAudit(ctx context.Context, e agentevents.ModelCallCompletedEvent) error {
	attrs := []any{"provider", e.Provider, "model", e.Model, "elapsed_ms", e.ElapsedMS}
	if e.ProviderError {
		return l.auditEvent(ctx, e.EventMeta, "llm_error", append(attrs, "error", e.Err.Error())...)
	}
	if !e.OutputReady {
		return nil
	}
	if u := e.Usage; u != nil {
		attrs = append(attrs, "prompt_tokens", u.PromptTokens, "completion_tokens", u.CompletionTokens, "total_tokens", u.TotalTokens, "cache_hit_tokens", u.CacheHitTokens)
	}
	return l.auditEvent(ctx, e.EventMeta, "llm_usage", attrs...)
}
func toolLogAttrs(e agentevents.ToolCallCompletedEvent) []any {
	r := e.Record
	return []any{"arguments", logArguments(e.Arguments), "tool", r.ToolName, "tool_call_id", r.ToolCallID, "actor_id", r.ActorID, "risk", r.RiskLevel, "success", r.Success, "elapsed_ms", r.FinishedAt.Sub(r.StartedAt).Milliseconds(), "error", r.Error}
}
func (l agentLogger) toolOutput(ctx context.Context, e agentevents.ToolCallCompletedEvent) error {
	var recordErr error
	if e.RecordErr != nil {
		recordErr = writeAgentLog(ctx, l.runtime, e.EventMeta, slog.LevelWarn, "record tool call failed", "tool", e.Record.ToolName, "error", e.RecordErr)
	}
	attrs := append([]any{"event", "tool_call", "result", e.Record.ResultPreview}, toolLogAttrs(e)...)
	return errors.Join(recordErr, writeAgentLog(ctx, l.runtime, e.EventMeta, slog.LevelInfo, "tool call", attrs...))
}
func (l agentLogger) toolAudit(ctx context.Context, e agentevents.ToolCallCompletedEvent) error {
	return l.auditEvent(ctx, e.EventMeta, "tool_call", toolLogAttrs(e)...)
}
func (l agentLogger) confirmation(ctx context.Context, e agentevents.ConfirmationChangedEvent) error {
	attrs := []any{"tool", e.Tool, "risk", e.Risk}
	var event string
	switch e.Phase {
	case "command":
		event = "risk_confirmation_command"
		attrs = append(attrs, "action", e.Action, "extra", e.Extra)
	case "wait":
		event = "risk_confirmation_wait"
		attrs = append(attrs, "arguments", logArguments(e.Arguments))
		if e.Reasons != "" {
			attrs = append(attrs, "risk_reasons", e.Reasons)
		}
	case "result":
		event = "risk_confirmation_result"
		attrs = append(attrs, "action", e.Action, "extra", e.Extra, "reason", e.Reason)
	case "background":
		event = "background_shell_" + e.Action
		attrs = append(attrs, "kind", e.Kind, "sandbox_dir", e.SandboxDir, "arguments", logArguments(e.Arguments))
	default:
		return nil
	}
	return l.auditEvent(ctx, e.EventMeta, event, attrs...)
}
func (l agentLogger) denied(ctx context.Context, e agentevents.ToolDeniedEvent) error {
	return l.auditEvent(ctx, e.EventMeta, "permission_denied", "actor_id", e.ActorID, "tool", e.Tool, "risk", e.Risk, "reason", e.Reason)
}
func (l agentLogger) hookFailure(ctx context.Context, e agentevents.HookFailedEvent) error {
	if !e.Log {
		return nil
	}
	level, message := slog.LevelWarn, "hook error"
	if errors.Is(e.Err, context.Canceled) {
		level, message = slog.LevelInfo, "hook canceled"
	}
	return writeAgentLog(ctx, l.runtime, e.EventMeta, level, message, "point", string(e.Point), "error", e.Err.Error())
}
func (l agentLogger) deliveryAudit(ctx context.Context, e agentevents.ReplyDeliveredEvent) error {
	if e.Err == nil || !e.Buffered || e.Operation != "send_assistant_message" {
		return nil
	}
	return l.auditEvent(ctx, e.EventMeta, "platform_send_error", "operation", e.Operation, "error", e.Err.Error())
}
func (l agentLogger) deliveryOutput(ctx context.Context, e agentevents.ReplyDeliveredEvent) error {
	return writeAgentLog(ctx, l.runtime, e.EventMeta, slog.LevelDebug, "reply delivered", "operation", e.Operation, "platform_message_ids", e.Receipt.PlatformMessageIDs, "sent_messages", e.Receipt.SentMessages, "error", e.Err)
}
func (l agentLogger) commitAudit(ctx context.Context, e agentevents.ReplyCommittedEvent) error {
	var errs []error
	if e.PersistErr != nil {
		errs = append(errs, l.auditEvent(ctx, e.EventMeta, "persistence_error", "operation", "append_assistant_message", "error", e.PersistErr.Error()))
	}
	for _, err := range e.AssociationErrors {
		var failure agentevents.AssociationFailure
		if errors.As(err, &failure) {
			errs = append(errs, l.auditEvent(ctx, e.EventMeta, "persistence_error", "operation", "map_platform_message", "platform_message_id", failure.PlatformMessageID, "error", failure.Err.Error()))
		}
	}
	return errors.Join(errs...)
}
func (l agentLogger) commitOutput(ctx context.Context, e agentevents.ReplyCommittedEvent) error {
	var errs []error
	for _, err := range e.AssociationErrors {
		var failure agentevents.AssociationFailure
		if errors.As(err, &failure) {
			errs = append(errs, writeAgentLog(ctx, l.runtime, e.EventMeta, slog.LevelWarn, "map platform message failed", "platform_message_id", failure.PlatformMessageID, "error", failure.Err.Error()))
		}
	}
	errs = append(errs, writeAgentLog(ctx, l.runtime, e.EventMeta, slog.LevelDebug, "reply committed", "message_id", e.MessageID, "persisted", e.Persisted, "persistence_error", e.PersistErr, "association_errors", e.AssociationErrors, "receipt", e.Receipt, "error", e.Err))
	return errors.Join(errs...)
}
