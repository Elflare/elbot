package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"elbot/internal/agent"
)

type agentLogger struct{ runtime, audit *slog.Logger }

func (l agentLogger) userInput(ctx context.Context, e agent.UserInputReceivedEvent) error {
	return writeLog(ctx, l.runtime, e.At, slog.LevelInfo, "user input", "event", "user_message", "session_id", e.SessionID, "text", logPreview(e.Text, 120))
}
func (l agentLogger) persistence(ctx context.Context, e agent.PersistenceFailedEvent) error {
	return l.auditEvent(ctx, e.EventMeta, "persistence_error", "session_id", e.SessionID, "operation", e.Operation, "error", e.Err.Error())
}
func (l agentLogger) timeoutOutput(ctx context.Context, e agent.TurnTimedOutEvent) error {
	return writeLog(ctx, l.runtime, e.At, slog.LevelWarn, "turn response timeout", "session_id", e.SessionID, "error", e.Err.Error())
}
func (l agentLogger) timeoutAudit(ctx context.Context, e agent.TurnTimedOutEvent) error {
	return l.auditEvent(ctx, e.EventMeta, "turn_response_timeout", "session_id", e.SessionID, "error", e.Err.Error())
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
func (l agentLogger) auditEvent(ctx context.Context, meta agent.EventMeta, event string, attrs ...any) error {
	return writeLog(ctx, l.audit, meta.At, slog.LevelInfo, "audit event", append([]any{"event", event}, attrs...)...)
}
func (l agentLogger) modelOutput(ctx context.Context, e agent.ModelCallCompletedEvent) error {
	if !e.OutputReady {
		return nil
	}
	return writeLog(ctx, l.runtime, e.At, slog.LevelInfo, "llm output", "event", "assistant_message", "session_id", e.SessionID, "provider", e.Provider, "model", e.Model, "elapsed_ms", e.ElapsedMS, "text", logPreview(e.Text, 120), "raw_text", logPreview(e.SourceText, 120), "tool_call_count", e.ToolCallCount)
}
func (l agentLogger) modelAudit(ctx context.Context, e agent.ModelCallCompletedEvent) error {
	attrs := []any{"session_id", e.SessionID, "provider", e.Provider, "model", e.Model, "elapsed_ms", e.ElapsedMS}
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
func toolLogAttrs(e agent.ToolCallCompletedEvent) []any {
	r := e.Record
	return []any{"session_id", e.SessionID, "arguments", logArguments(e.Arguments), "tool", r.ToolName, "tool_call_id", r.ToolCallID, "actor_id", r.ActorID, "risk", r.RiskLevel, "success", r.Success, "elapsed_ms", r.FinishedAt.Sub(r.StartedAt).Milliseconds(), "error", r.Error}
}
func (l agentLogger) toolOutput(ctx context.Context, e agent.ToolCallCompletedEvent) error {
	var recordErr error
	if e.RecordErr != nil {
		recordErr = writeLog(ctx, l.runtime, e.At, slog.LevelWarn, "record tool call failed", "session_id", e.SessionID, "tool", e.Record.ToolName, "error", e.RecordErr)
	}
	attrs := append([]any{"event", "tool_call", "result", e.Record.ResultPreview}, toolLogAttrs(e)...)
	return errors.Join(recordErr, writeLog(ctx, l.runtime, e.At, slog.LevelInfo, "tool call", attrs...))
}
func (l agentLogger) toolAudit(ctx context.Context, e agent.ToolCallCompletedEvent) error {
	return l.auditEvent(ctx, e.EventMeta, "tool_call", toolLogAttrs(e)...)
}
func (l agentLogger) confirmation(ctx context.Context, e agent.ConfirmationChangedEvent) error {
	attrs := []any{"session_id", e.SessionID, "tool", e.Tool, "risk", e.Risk}
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
func (l agentLogger) denied(ctx context.Context, e agent.ToolDeniedEvent) error {
	return l.auditEvent(ctx, e.EventMeta, "permission_denied", "actor_id", e.ActorID, "session_id", e.SessionID, "tool", e.Tool, "risk", e.Risk, "reason", e.Reason)
}
func (l agentLogger) hookFailure(ctx context.Context, e agent.HookFailedEvent) error {
	if !e.Log {
		return nil
	}
	level, message := slog.LevelWarn, "hook error"
	if errors.Is(e.Err, context.Canceled) {
		level, message = slog.LevelInfo, "hook canceled"
	}
	return writeLog(ctx, l.runtime, e.At, level, message, "point", string(e.Point), "error", e.Err.Error())
}
func (l agentLogger) deliveryAudit(ctx context.Context, e agent.ReplyDeliveredEvent) error {
	if e.Err == nil || !e.Buffered || e.Operation != "send_assistant_message" {
		return nil
	}
	return l.auditEvent(ctx, e.EventMeta, "platform_send_error", "session_id", e.SessionID, "operation", e.Operation, "error", e.Err.Error())
}
func (l agentLogger) deliveryOutput(ctx context.Context, e agent.ReplyDeliveredEvent) error {
	return writeLog(ctx, l.runtime, e.At, slog.LevelDebug, "reply delivered", "session_id", e.SessionID, "operation", e.Operation, "platform_message_ids", e.Receipt.PlatformMessageIDs, "sent_messages", e.Receipt.SentMessages, "error", e.Err)
}
func (l agentLogger) commitAudit(ctx context.Context, e agent.ReplyCommittedEvent) error {
	var errs []error
	if e.PersistErr != nil {
		errs = append(errs, l.auditEvent(ctx, e.EventMeta, "persistence_error", "session_id", e.SessionID, "operation", "append_assistant_message", "error", e.PersistErr.Error()))
	}
	for _, err := range e.AssociationErrors {
		var failure agent.AssociationFailure
		if errors.As(err, &failure) {
			errs = append(errs, l.auditEvent(ctx, e.EventMeta, "persistence_error", "session_id", e.SessionID, "operation", "map_platform_message", "platform_message_id", failure.PlatformMessageID, "error", failure.Err.Error()))
		}
	}
	return errors.Join(errs...)
}
func (l agentLogger) commitOutput(ctx context.Context, e agent.ReplyCommittedEvent) error {
	var errs []error
	for _, err := range e.AssociationErrors {
		var failure agent.AssociationFailure
		if errors.As(err, &failure) {
			errs = append(errs, writeLog(ctx, l.runtime, e.At, slog.LevelWarn, "map platform message failed", "session_id", e.SessionID, "platform_message_id", failure.PlatformMessageID, "error", failure.Err.Error()))
		}
	}
	errs = append(errs, writeLog(ctx, l.runtime, e.At, slog.LevelDebug, "reply committed", "session_id", e.SessionID, "message_id", e.MessageID, "persisted", e.Persisted, "persistence_error", e.PersistErr, "association_errors", e.AssociationErrors, "receipt", e.Receipt, "error", e.Err))
	return errors.Join(errs...)
}
