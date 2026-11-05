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
	record.Identity = events.LogIdentityFromExecution(execution)
	events.Log(context.WithoutCancel(ctx), record)
}

// TruncateLogContent 只截断日志文本；limit 是包含省略号的正字符数上限。
func TruncateLogContent(text string, limit int) string {
	return logging.TruncateContent(text, limit)
}

// ReportFailure records termination, without repeating the owning operation diagnostic.
// Callers settle cancellation, recovery and execution replacement before calling.
// notify preserves foreground/background delivery policy without coupling steps
// to notification transport or adding an "already reported" error marker.
func ReportFailure(ctx context.Context, sessionID string, err error, notify func(context.Context, slog.Level, string)) {
	record := events.LogRecord{
		Category: events.LogRuntime, Level: slog.LevelWarn,
		EventName: "dialogue_failed", Module: "dialogue", Type: events.ContentText,
		Content: "dialogue terminated", ResultStatus: events.ResultFailed,
	}
	facts, _ := contextinfo.ExecutionFromContext(ctx)
	record.Identity = events.LogIdentityFromExecution(facts)
	record.Identity.SessionID = sessionID
	events.Log(context.WithoutCancel(ctx), record)
	notify(session.WithBinding(context.WithoutCancel(ctx), nil), slog.LevelWarn, "对话处理失败："+err.Error())
}
