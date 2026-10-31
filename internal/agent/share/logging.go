package share

import (
	"context"
	"elbot/internal/logging"
	"elbot/internal/session"
	"log/slog"

	"elbot/internal/contextinfo"
	"elbot/internal/events"
)

// EmitAgentLog copies only the source's explicit execution identity.
func EmitAgentLog(ctx context.Context, execution contextinfo.Execution, record events.LogRecord) {
	if execution.SessionID != "" {
		record.Identity.SessionID = &execution.SessionID
	}
	if execution.RunID != "" {
		record.Identity.RunID = &execution.RunID
	}
	if execution.Attempt != "" {
		record.Identity.Attempt = &execution.Attempt
	}
	if execution.RequestID != "" {
		record.Identity.RequestID = &execution.RequestID
	}
	if execution.RootRequestID != "" {
		record.Identity.RootRequestID = &execution.RootRequestID
	}
	_ = events.EmitLog(context.WithoutCancel(ctx), record)
}

// TruncateLogContent 只截断日志文本；limit 是包含省略号的正字符数上限。
func TruncateLogContent(text string, limit int) string {
	return logging.TruncateContent(text, limit)
}

// ReportFailure reports a terminal business failure at its owning entrypoint.
// Callers settle cancellation, recovery and execution replacement before calling.
// notify preserves foreground/background delivery policy without coupling steps
// to notification transport or adding an "already reported" error marker.
func ReportFailure(ctx context.Context, sessionID string, err error, notify func(context.Context, slog.Level, string)) {
	record := events.LogRecord{
		Category: events.LogRuntime, Level: slog.LevelError,
		EventName: "dialogue_failed", Module: "dialogue", Type: events.ContentText,
		Content: err.Error(), ContentLimit: 8192, ResultStatus: events.ResultFailed,
	}
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	if sessionID != "" {
		record.Identity.SessionID = &sessionID
	}
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
	notify(session.WithBinding(context.WithoutCancel(ctx), nil), slog.LevelError, "对话处理失败："+err.Error())
}
