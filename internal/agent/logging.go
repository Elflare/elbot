package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	agentevents "elbot/internal/agent/events"
	"elbot/internal/events"
	"elbot/internal/signal"
)

// emitAgentLog preserves the identity fixed by the source event, including
// absent identities. A failed observation never changes a business result.
func emitAgentLog(ctx context.Context, category events.LogCategory, meta agentevents.EventMeta, level slog.Level, message string, attrs ...any) error {
	record := events.LogRecord{At: meta.At, Category: category, Level: level, Name: strings.ReplaceAll(message, " ", "_"), Module: "agent", Summary: message}
	record.Fields = []slog.Attr{
		slog.String("session_id", meta.SessionID),
		slog.String("run_id", meta.RunID),
		slog.String("attempt", meta.Attempt),
		slog.String("request_id", meta.RequestID),
		slog.String("root_request_id", meta.RootRequestID),
	}
	details := map[string]any{}
	for _, attr := range slog.Group("", attrs...).Value.Group() {
		if attr.Key == "event" {
			record.Name = attr.Value.String()
			continue
		}
		if category == events.LogRuntime {
			switch attr.Key {
			case "text", "raw_text", "arguments", "result", "receipt":
				details[attr.Key] = attr.Value.Any()
				if attr.Key == "text" || attr.Key == "result" {
					record.Summary += ": " + attr.Value.String()
				}
				continue
			}
		}
		if attr.Key == "upstream_detail" {
			record.Detail = attr.Value.String()
			continue
		}
		record.Fields = append(record.Fields, attr)
	}
	if len(details) > 0 {
		data, _ := json.Marshal(details)
		record.Detail = string(data)
	}
	_ = events.EmitLog(ctx, record)
	return nil
}

type agentLogProjection struct{}

func (a *Agent) connectLogSignals() error {
	l := agentLogProjection{}
	signals := a.signals
	connections := []func() (*signal.Connection, error){
		func() (*signal.Connection, error) {
			return signals.UserInputReceived.Connect(l.userInput, signal.ConnectOptions{})
		},
		func() (*signal.Connection, error) {
			return signals.PersistenceFailed.Connect(l.persistence, signal.ConnectOptions{})
		},
		func() (*signal.Connection, error) {
			return signals.TurnTimedOut.Connect(l.timeoutOutput, signal.ConnectOptions{})
		},
		func() (*signal.Connection, error) {
			return signals.TurnTimedOut.Connect(l.timeoutAudit, signal.ConnectOptions{})
		},
		func() (*signal.Connection, error) {
			return signals.ModelCallCompleted.Connect(l.modelOutput, signal.ConnectOptions{})
		},
		func() (*signal.Connection, error) {
			return signals.ModelCallCompleted.Connect(l.modelAudit, signal.ConnectOptions{})
		},
		func() (*signal.Connection, error) {
			return signals.ToolCallCompleted.Connect(l.toolOutput, signal.ConnectOptions{})
		},
		func() (*signal.Connection, error) {
			return signals.ToolCallCompleted.Connect(l.toolAudit, signal.ConnectOptions{})
		},
		func() (*signal.Connection, error) {
			return signals.ConfirmationChanged.Connect(l.confirmation, signal.ConnectOptions{})
		},
		func() (*signal.Connection, error) {
			return signals.ToolDenied.Connect(l.denied, signal.ConnectOptions{})
		},
		func() (*signal.Connection, error) {
			return signals.ReplyDelivered.Connect(l.deliveryOutput, signal.ConnectOptions{})
		},
		func() (*signal.Connection, error) {
			return signals.ReplyDelivered.Connect(l.deliveryAudit, signal.ConnectOptions{})
		},
		func() (*signal.Connection, error) {
			return signals.ReplyCommitted.Connect(l.commitOutput, signal.ConnectOptions{})
		},
		func() (*signal.Connection, error) {
			return signals.ReplyCommitted.Connect(l.commitAudit, signal.ConnectOptions{})
		},
	}
	for _, connect := range connections {
		connection, err := connect()
		if err != nil {
			a.disconnectLogSignals()
			return err
		}
		a.logConnections = append(a.logConnections, connection)
	}
	return nil
}
func (a *Agent) disconnectLogSignals() {
	for _, connection := range a.logConnections {
		connection.Disconnect()
	}
}

func (l agentLogProjection) userInput(ctx context.Context, e agentevents.UserInputReceivedEvent) error {
	return emitAgentLog(ctx, events.LogRuntime, e.EventMeta, slog.LevelInfo, "user input", "event", "user_message", "text", e.Text)
}
func (l agentLogProjection) persistence(ctx context.Context, e agentevents.PersistenceFailedEvent) error {
	return l.auditEvent(ctx, e.EventMeta, slog.LevelError, "persistence_error", "operation", e.Operation, "error", e.Err.Error())
}
func (l agentLogProjection) timeoutOutput(ctx context.Context, e agentevents.TurnTimedOutEvent) error {
	return emitAgentLog(ctx, events.LogRuntime, e.EventMeta, slog.LevelWarn, "turn response timeout", "error", e.Err.Error())
}
func (l agentLogProjection) timeoutAudit(ctx context.Context, e agentevents.TurnTimedOutEvent) error {
	return l.auditEvent(ctx, e.EventMeta, slog.LevelWarn, "turn_response_timeout", "error", e.Err.Error())
}

func (l agentLogProjection) auditEvent(ctx context.Context, meta agentevents.EventMeta, level slog.Level, event string, attrs ...any) error {
	return emitAgentLog(ctx, events.LogAudit, meta, level, "audit event", append([]any{"event", event}, attrs...)...)
}
func (l agentLogProjection) modelOutput(ctx context.Context, e agentevents.ModelCallCompletedEvent) error {
	if !e.OutputReady {
		return nil
	}
	return emitAgentLog(ctx, events.LogRuntime, e.EventMeta, slog.LevelInfo, "llm output", "event", "assistant_message", "provider", e.Provider, "model", e.Model, "elapsed_ms", e.ElapsedMS, "text", e.Text, "raw_text", e.SourceText, "tool_call_count", e.ToolCallCount)
}
func (l agentLogProjection) modelAudit(ctx context.Context, e agentevents.ModelCallCompletedEvent) error {
	attrs := []any{"provider", e.Provider, "model", e.Model, "elapsed_ms", e.ElapsedMS}
	if e.ProviderError {
		var diagnostic events.DiagnosticError
		if errors.As(e.Err, &diagnostic) {
			value := diagnostic.LogDiagnostic()
			attrs = append(attrs, "upstream_event", value.Kind, "upstream_detail", value.Detail)
		}
		return l.auditEvent(ctx, e.EventMeta, slog.LevelWarn, "llm_error", append(attrs, "error", e.Err.Error())...)
	}
	if !e.OutputReady {
		return nil
	}
	if u := e.Usage; u != nil {
		attrs = append(attrs, "prompt_tokens", u.PromptTokens, "completion_tokens", u.CompletionTokens, "total_tokens", u.TotalTokens, "cache_hit_tokens", u.CacheHitTokens)
	}
	return l.auditEvent(ctx, e.EventMeta, slog.LevelInfo, "llm_usage", attrs...)
}
func toolLogAttrs(e agentevents.ToolCallCompletedEvent) []any {
	r := e.Record
	return []any{
		"arguments",
		e.Arguments,
		"tool",
		r.ToolName,
		"tool_call_id",
		r.ToolCallID,
		"actor_id",
		r.ActorID,
		"risk",
		r.RiskLevel,
		"success",
		r.Success,
		"elapsed_ms",
		r.FinishedAt.Sub(r.StartedAt).Milliseconds(),
		"error",
		r.Error,
	}
}
func (l agentLogProjection) toolOutput(ctx context.Context, e agentevents.ToolCallCompletedEvent) error {
	var recordErr error
	if e.RecordErr != nil {
		recordErr = emitAgentLog(ctx, events.LogRuntime, e.EventMeta, slog.LevelError, "record tool call failed", "tool", e.Record.ToolName, "error", e.RecordErr)
	}
	attrs := append([]any{"event", "tool_call", "result", e.Record.ResultPreview}, toolLogAttrs(e)...)
	level := slog.LevelInfo
	if !e.Record.Success {
		level = slog.LevelWarn
	}
	return errors.Join(recordErr, emitAgentLog(ctx, events.LogRuntime, e.EventMeta, level, "tool call", attrs...))
}
func (l agentLogProjection) toolAudit(ctx context.Context, e agentevents.ToolCallCompletedEvent) error {
	level := slog.LevelInfo
	if !e.Record.Success {
		level = slog.LevelWarn
	}
	return l.auditEvent(ctx, e.EventMeta, level, "tool_call", toolLogAttrs(e)...)
}
func (l agentLogProjection) confirmation(ctx context.Context, e agentevents.ConfirmationChangedEvent) error {
	attrs := []any{"tool", e.Tool, "risk", e.Risk}
	var event string
	switch e.Phase {
	case "command":
		event = "risk_confirmation_command"
		attrs = append(attrs, "action", e.Action, "extra", e.Extra)
	case "wait":
		event = "risk_confirmation_wait"
		attrs = append(attrs, "arguments", e.Arguments)
		if e.Reasons != "" {
			attrs = append(attrs, "risk_reasons", e.Reasons)
		}
	case "result":
		event = "risk_confirmation_result"
		attrs = append(attrs, "action", e.Action, "extra", e.Extra, "reason", e.Reason)
	case "background":
		event = "background_shell_" + e.Action
		attrs = append(attrs, "kind", e.Kind, "sandbox_dir", e.SandboxDir, "arguments", e.Arguments)
	default:
		return nil
	}
	return l.auditEvent(ctx, e.EventMeta, slog.LevelInfo, event, attrs...)
}
func (l agentLogProjection) denied(ctx context.Context, e agentevents.ToolDeniedEvent) error {
	return l.auditEvent(ctx, e.EventMeta, slog.LevelWarn, "permission_denied", "actor_id", e.ActorID, "tool", e.Tool, "risk", e.Risk, "reason", e.Reason)
}
func (l agentLogProjection) deliveryAudit(ctx context.Context, e agentevents.ReplyDeliveredEvent) error {
	if e.Err == nil || !e.Buffered || e.Operation != "send_assistant_message" {
		return nil
	}
	return l.auditEvent(ctx, e.EventMeta, slog.LevelWarn, "platform_send_error", "operation", e.Operation, "error", e.Err.Error())
}
func (l agentLogProjection) deliveryOutput(ctx context.Context, e agentevents.ReplyDeliveredEvent) error {
	return emitAgentLog(ctx, events.LogRuntime, e.EventMeta, slog.LevelDebug, "reply delivered", "operation", e.Operation, "platform_message_ids", e.Receipt.PlatformMessageIDs, "sent_messages", e.Receipt.SentMessages, "error", e.Err)
}
func (l agentLogProjection) commitAudit(ctx context.Context, e agentevents.ReplyCommittedEvent) error {
	var errs []error
	if e.PersistErr != nil {
		errs = append(errs, l.auditEvent(ctx, e.EventMeta, slog.LevelError, "persistence_error", "operation", "append_assistant_message", "error", e.PersistErr.Error()))
	}
	for _, err := range e.AssociationErrors {
		var failure agentevents.AssociationFailure
		if errors.As(err, &failure) {
			errs = append(errs, l.auditEvent(ctx, e.EventMeta, slog.LevelError, "persistence_error", "operation", "map_platform_message", "platform_message_id", failure.PlatformMessageID, "error", failure.Err.Error()))
		}
	}
	return errors.Join(errs...)
}
func (l agentLogProjection) commitOutput(ctx context.Context, e agentevents.ReplyCommittedEvent) error {
	var errs []error
	for _, err := range e.AssociationErrors {
		var failure agentevents.AssociationFailure
		if errors.As(err, &failure) {
			errs = append(errs, emitAgentLog(ctx, events.LogRuntime, e.EventMeta, slog.LevelError, "map platform message failed", "platform_message_id", failure.PlatformMessageID, "error", failure.Err.Error()))
		}
	}
	errs = append(errs, emitAgentLog(ctx, events.LogRuntime, e.EventMeta, slog.LevelDebug, "reply committed", "message_id", e.MessageID, "persisted", e.Persisted, "persistence_error", e.PersistErr, "association_errors", e.AssociationErrors, "receipt", e.Receipt, "error", e.Err))
	return errors.Join(errs...)
}
