package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	agentevents "elbot/internal/agent/events"
	"elbot/internal/events"
	"elbot/internal/signal"
)

// emitAgentLog preserves the identity fixed by the source event, including
// absent identities. A failed observation never changes a business result.
func emitAgentLog(ctx context.Context, category events.LogCategory, meta agentevents.EventMeta, level slog.Level, eventName, summary string, attrs ...any) {
	record := events.LogRecord{At: meta.At, Category: category, Level: level, Name: eventName, Module: "agent", Summary: summary}
	record.Fields = []slog.Attr{
		slog.String("session_id", meta.SessionID),
		slog.String("run_id", meta.RunID),
		slog.String("attempt", meta.Attempt),
		slog.String("request_id", meta.RequestID),
		slog.String("root_request_id", meta.RootRequestID),
	}
	details := map[string]any{}
	for _, attr := range slog.Group("", attrs...).Value.Group() {
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
}

type agentLogProjection struct{}

func (a *Agent) connectLogSignals() error {
	l := agentLogProjection{}
	signals := a.signals
	connections := []func() (*signal.Connection, error){
		func() (*signal.Connection, error) {
			return signals.UserInputReceived.Connect(l.userInputRuntimeLog, signal.ConnectOptions{})
		},
		func() (*signal.Connection, error) {
			return signals.PersistenceFailed.Connect(l.persistenceAuditLog, signal.ConnectOptions{})
		},
		func() (*signal.Connection, error) {
			return signals.TurnTimedOut.Connect(l.timeoutRuntimeLog, signal.ConnectOptions{})
		},
		func() (*signal.Connection, error) {
			return signals.TurnTimedOut.Connect(l.timeoutAuditLog, signal.ConnectOptions{})
		},
		func() (*signal.Connection, error) {
			return signals.ModelCallCompleted.Connect(l.modelRuntimeLog, signal.ConnectOptions{})
		},
		func() (*signal.Connection, error) {
			return signals.ModelCallCompleted.Connect(l.modelAuditLog, signal.ConnectOptions{})
		},
		func() (*signal.Connection, error) {
			return signals.ToolCallCompleted.Connect(l.toolRuntimeLog, signal.ConnectOptions{})
		},
		func() (*signal.Connection, error) {
			return signals.ToolCallCompleted.Connect(l.toolAuditLog, signal.ConnectOptions{})
		},
		func() (*signal.Connection, error) {
			return signals.ConfirmationChanged.Connect(l.confirmationAuditLog, signal.ConnectOptions{})
		},
		func() (*signal.Connection, error) {
			return signals.ToolDenied.Connect(l.deniedAuditLog, signal.ConnectOptions{})
		},
		func() (*signal.Connection, error) {
			return signals.ReplyDelivered.Connect(l.deliveryRuntimeLog, signal.ConnectOptions{})
		},
		func() (*signal.Connection, error) {
			return signals.ReplyDelivered.Connect(l.deliveryAuditLog, signal.ConnectOptions{})
		},
		func() (*signal.Connection, error) {
			return signals.ReplyCommitted.Connect(l.commitRuntimeLog, signal.ConnectOptions{})
		},
		func() (*signal.Connection, error) {
			return signals.ReplyCommitted.Connect(l.commitAuditLog, signal.ConnectOptions{})
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

func (l agentLogProjection) userInputRuntimeLog(ctx context.Context, e agentevents.UserInputReceivedEvent) error {
	emitAgentLog(ctx, events.LogRuntime, e.EventMeta, slog.LevelInfo, "user_message", "user input", "text", e.Text)
	return nil
}
func (l agentLogProjection) persistenceAuditLog(ctx context.Context, e agentevents.PersistenceFailedEvent) error {
	l.auditEvent(ctx, e.EventMeta, failureLogLevel(e.Err), "persistence_error", "persistence failed", "operation", e.Operation, "error", e.Err.Error())
	return nil
}
func (l agentLogProjection) timeoutRuntimeLog(ctx context.Context, e agentevents.TurnTimedOutEvent) error {
	emitAgentLog(ctx, events.LogRuntime, e.EventMeta, slog.LevelError, "turn_response_timeout", "turn response timeout", "error", e.Err.Error())
	return nil
}
func (l agentLogProjection) timeoutAuditLog(ctx context.Context, e agentevents.TurnTimedOutEvent) error {
	l.auditEvent(ctx, e.EventMeta, slog.LevelError, "turn_response_timeout", "turn response timeout", "error", e.Err.Error())
	return nil
}

func (l agentLogProjection) auditEvent(ctx context.Context, meta agentevents.EventMeta, level slog.Level, eventName, summary string, attrs ...any) {
	emitAgentLog(ctx, events.LogAudit, meta, level, eventName, summary, attrs...)
}
func (l agentLogProjection) modelRuntimeLog(ctx context.Context, e agentevents.ModelCallCompletedEvent) error {
	if !e.OutputReady {
		return nil
	}
	emitAgentLog(ctx, events.LogRuntime, e.EventMeta, slog.LevelInfo, "assistant_message", "llm output", "provider", e.Provider, "model", e.Model, "elapsed_ms", e.ElapsedMS, "text", e.Text, "raw_text", e.SourceText, "tool_call_count", e.ToolCallCount)
	return nil
}
func (l agentLogProjection) modelAuditLog(ctx context.Context, e agentevents.ModelCallCompletedEvent) error {
	attrs := []any{"provider", e.Provider, "model", e.Model, "elapsed_ms", e.ElapsedMS}
	if e.ProviderError {
		var diagnostic events.DiagnosticError
		if errors.As(e.Err, &diagnostic) {
			value := diagnostic.LogDiagnostic()
			attrs = append(attrs, "upstream_event", value.Kind, "upstream_detail", value.Detail)
		}
		l.auditEvent(ctx, e.EventMeta, failureLogLevel(e.Err), "llm_error", "model call failed", append(attrs, "error", e.Err.Error())...)
		return nil
	}
	if !e.OutputReady {
		return nil
	}
	if u := e.Usage; u != nil {
		attrs = append(attrs, "prompt_tokens", u.PromptTokens, "completion_tokens", u.CompletionTokens, "total_tokens", u.TotalTokens, "cache_hit_tokens", u.CacheHitTokens)
	}
	l.auditEvent(ctx, e.EventMeta, slog.LevelInfo, "llm_usage", "model usage", attrs...)
	return nil
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
func failureLogLevel(err error) slog.Level {
	if errors.Is(err, context.Canceled) {
		return slog.LevelInfo
	}
	return slog.LevelError
}

func toolLogLevel(event agentevents.ToolCallCompletedEvent) slog.Level {
	if event.PolicyDenied {
		return slog.LevelWarn
	}
	if event.Record.Success {
		return slog.LevelInfo
	}
	return failureLogLevel(event.CallErr)
}

func (l agentLogProjection) toolRuntimeLog(ctx context.Context, e agentevents.ToolCallCompletedEvent) error {
	if e.RecordErr != nil {
		emitAgentLog(ctx, events.LogRuntime, e.EventMeta, failureLogLevel(e.RecordErr), "record_tool_call_failed", "record tool call failed", "tool", e.Record.ToolName, "error", e.RecordErr)
	}
	fields := append([]any{"result", e.Record.ResultPreview}, toolLogAttrs(e)...)
	emitAgentLog(ctx, events.LogRuntime, e.EventMeta, toolLogLevel(e), "tool_call", "tool call", fields...)
	return nil
}
func (l agentLogProjection) toolAuditLog(ctx context.Context, e agentevents.ToolCallCompletedEvent) error {
	l.auditEvent(ctx, e.EventMeta, toolLogLevel(e), "tool_call", "tool call", toolLogAttrs(e)...)
	return nil
}
func (l agentLogProjection) confirmationAuditLog(ctx context.Context, e agentevents.ConfirmationChangedEvent) error {
	attrs := []any{"tool", e.Tool, "risk", e.Risk}
	var eventName string
	switch e.Phase {
	case "command":
		eventName = "risk_confirmation_command"
		attrs = append(attrs, "action", e.Action, "extra", e.Extra)
	case "wait":
		eventName = "risk_confirmation_wait"
		attrs = append(attrs, "arguments", e.Arguments)
		if e.Reasons != "" {
			attrs = append(attrs, "risk_reasons", e.Reasons)
		}
	case "result":
		eventName = "risk_confirmation_result"
		attrs = append(attrs, "action", e.Action, "extra", e.Extra, "reason", e.Reason)
	case "background":
		eventName = "background_shell_" + e.Action
		attrs = append(attrs, "kind", e.Kind, "sandbox_dir", e.SandboxDir, "arguments", e.Arguments)
	default:
		return nil
	}
	l.auditEvent(ctx, e.EventMeta, slog.LevelInfo, eventName, "confirmation changed", attrs...)
	return nil
}
func (l agentLogProjection) deniedAuditLog(ctx context.Context, e agentevents.ToolDeniedEvent) error {
	l.auditEvent(ctx, e.EventMeta, slog.LevelWarn, "permission_denied", "permission denied", "actor_id", e.ActorID, "tool", e.Tool, "risk", e.Risk, "reason", e.Reason)
	return nil
}
func (l agentLogProjection) deliveryAuditLog(ctx context.Context, e agentevents.ReplyDeliveredEvent) error {
	if e.Err == nil || !e.Buffered || e.Operation != "send_assistant_message" {
		return nil
	}
	l.auditEvent(ctx, e.EventMeta, failureLogLevel(e.Err), "platform_send_error", "platform send failed", "operation", e.Operation, "error", e.Err.Error())
	return nil
}
func (l agentLogProjection) deliveryRuntimeLog(ctx context.Context, e agentevents.ReplyDeliveredEvent) error {
	level := slog.LevelDebug
	if e.Err != nil {
		level = failureLogLevel(e.Err)
	}
	emitAgentLog(ctx, events.LogRuntime, e.EventMeta, level, "reply_delivered", "reply delivered", "operation", e.Operation, "platform_message_ids", e.Receipt.PlatformMessageIDs, "sent_messages", e.Receipt.SentMessages, "error", e.Err)
	return nil
}
func (l agentLogProjection) commitAuditLog(ctx context.Context, e agentevents.ReplyCommittedEvent) error {
	if e.PersistErr != nil {
		l.auditEvent(ctx, e.EventMeta, failureLogLevel(e.PersistErr), "persistence_error", "persistence failed", "operation", "append_assistant_message", "error", e.PersistErr.Error())
	}
	for _, err := range e.AssociationErrors {
		var failure agentevents.AssociationFailure
		if errors.As(err, &failure) {
			l.auditEvent(ctx, e.EventMeta, failureLogLevel(failure.Err), "persistence_error", "persistence failed", "operation", "map_platform_message", "platform_message_id", failure.PlatformMessageID, "error", failure.Err.Error())
		}
	}
	return nil
}
func (l agentLogProjection) commitRuntimeLog(ctx context.Context, e agentevents.ReplyCommittedEvent) error {
	for _, err := range e.AssociationErrors {
		var failure agentevents.AssociationFailure
		if errors.As(err, &failure) {
			emitAgentLog(ctx, events.LogRuntime, e.EventMeta, failureLogLevel(failure.Err), "map_platform_message_failed", "map platform message failed", "platform_message_id", failure.PlatformMessageID, "error", failure.Err.Error())
		}
	}
	emitAgentLog(ctx, events.LogRuntime, e.EventMeta, slog.LevelDebug, "reply_committed", "reply committed", "message_id", e.MessageID, "persisted", e.Persisted, "persistence_error", e.PersistErr, "association_errors", e.AssociationErrors, "receipt", e.Receipt, "error", e.Err)
	return nil
}
