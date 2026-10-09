package agent

import (
	"context"
	agentevents "elbot/internal/agent/events"
	"elbot/internal/events"
	"elbot/internal/signal"
	"encoding/json"
	"fmt"
	"log/slog"
)

// emitAgentLog copies only the source's explicit event identity.
func emitAgentLog(ctx context.Context, meta agentevents.EventMeta, record events.LogRecord) {
	if meta.SessionID != "" {
		record.Identity.SessionID = &meta.SessionID
	}
	if meta.RunID != "" {
		record.Identity.RunID = &meta.RunID
	}
	if meta.Attempt != "" {
		record.Identity.Attempt = &meta.Attempt
	}
	if meta.RequestID != "" {
		record.Identity.RequestID = &meta.RequestID
	}
	if meta.RootRequestID != "" {
		record.Identity.RootRequestID = &meta.RootRequestID
	}
	_ = events.EmitLog(context.WithoutCancel(ctx), record)
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
			return signals.TurnTimedOut.Connect(l.timeoutRuntimeLog, signal.ConnectOptions{})
		},
		func() (*signal.Connection, error) {
			return signals.TurnTimedOut.Connect(l.timeoutAuditLog, signal.ConnectOptions{})
		},
		func() (*signal.Connection, error) {
			return signals.ConfirmationChanged.Connect(l.confirmationAuditLog, signal.ConnectOptions{})
		},
		func() (*signal.Connection, error) {
			return signals.ToolDenied.Connect(l.deniedAuditLog, signal.ConnectOptions{})
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
	emitAgentLog(ctx, e.EventMeta, events.LogRecord{Category: events.LogRuntime, Level: slog.LevelInfo, EventName: "user_message", Module: "agent", Type: events.ContentText, Content: e.Text, ContentLimit: 4096})
	return nil
}
func (l agentLogProjection) timeoutRuntimeLog(ctx context.Context, e agentevents.TurnTimedOutEvent) error {
	emitAgentLog(ctx, e.EventMeta, events.LogRecord{Category: events.LogRuntime, Level: slog.LevelError, EventName: "turn_response_timeout", Module: "agent", Type: events.ContentText, Content: fmt.Sprintf("turn response timeout: %v", e.Err), ContentLimit: 4096, ResultStatus: events.ResultFailed})
	return nil
}
func (l agentLogProjection) timeoutAuditLog(ctx context.Context, e agentevents.TurnTimedOutEvent) error {
	content, _ := json.Marshal(map[string]string{"error": fmt.Sprintf("%.2048v", e.Err)})
	emitAgentLog(ctx, e.EventMeta, events.LogRecord{Category: events.LogAudit, Level: slog.LevelError, EventName: "turn_response_timeout", Module: "agent", Type: events.ContentJSON, Content: string(content), ResultStatus: events.ResultFailed})
	return nil
}
func (l agentLogProjection) confirmationAuditLog(ctx context.Context, e agentevents.ConfirmationChangedEvent) error {
	content := map[string]string{"tool": e.Tool, "risk": e.Risk}
	var eventName string
	switch e.Phase {
	case "command":
		eventName = "risk_confirmation_command"
		content["action"], content["extra"] = e.Action, fmt.Sprintf("%.2048s", e.Extra)
	case "wait":
		eventName = "risk_confirmation_wait"
		content["arguments"], content["risk_reasons"] = fmt.Sprintf("%.2048s", e.Arguments), fmt.Sprintf("%.2048s", e.Reasons)
	case "result":
		eventName = "risk_confirmation_result"
		content["action"], content["extra"], content["reason"] = e.Action, fmt.Sprintf("%.2048s", e.Extra), fmt.Sprintf("%.2048s", e.Reason)
	case "background":
		eventName = "background_shell_" + e.Action
		content["kind"], content["sandbox_dir"], content["arguments"] = e.Kind, e.SandboxDir, fmt.Sprintf("%.2048s", e.Arguments)
	default:
		return nil
	}
	data, _ := json.Marshal(content)
	emitAgentLog(ctx, e.EventMeta, events.LogRecord{Category: events.LogAudit, Level: slog.LevelInfo, EventName: eventName, Module: "agent", Type: events.ContentJSON, Content: string(data)})
	return nil
}
func (l agentLogProjection) deniedAuditLog(ctx context.Context, e agentevents.ToolDeniedEvent) error {
	content, _ := json.Marshal(map[string]string{"actor_id": e.ActorID, "tool": e.Tool, "risk": e.Risk, "reason": fmt.Sprintf("%.2048s", e.Reason)})
	emitAgentLog(ctx, e.EventMeta, events.LogRecord{Category: events.LogAudit, Level: slog.LevelWarn, EventName: "permission_denied", Module: "agent", Type: events.ContentJSON, Content: string(content), ResultStatus: events.ResultRejected})
	return nil
}
