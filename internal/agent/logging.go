package agent

import (
	"context"
	"log/slog"
)

type LogManager interface {
	Runtime() *slog.Logger
	Audit() *slog.Logger
}

func (a *Agent) SetLogger(logger *slog.Logger) {
	a.logger = logger
	if a.hooks != nil {
		a.hooks.logger = logger
	}
	if a.output != nil {
		a.output.logger = logger
	}
	if a.replies != nil {
		a.replies.logger = logger
	}
}

func (a *Agent) SetLogManager(logs LogManager) {
	if logs == nil {
		a.SetLogger(nil)
		a.auditLogger = nil
		if a.replies != nil {
			a.replies.auditLogger = nil
		}
		return
	}
	a.SetLogger(logs.Runtime())
	a.auditLogger = logs.Audit()
	if a.replies != nil {
		a.replies.auditLogger = a.auditLogger
	}
}

func (a *Agent) audit(event string, attrs ...any) {
	a.auditLog(slog.LevelInfo, event, attrs...)
}

func (a *Agent) auditDebug(event string, attrs ...any) {
	a.auditLog(slog.LevelDebug, event, attrs...)
}

func (a *Agent) auditWarn(event string, attrs ...any) {
	a.auditLog(slog.LevelWarn, event, attrs...)
}

func (a *Agent) auditError(event string, attrs ...any) {
	a.auditLog(slog.LevelError, event, attrs...)
}

func (a *Agent) auditLog(level slog.Level, event string, attrs ...any) {
	writeAudit(a.auditLogger, level, event, attrs...)
}

func writeAudit(logger *slog.Logger, level slog.Level, event string, attrs ...any) {
	if logger == nil {
		return
	}
	attrs = append([]any{"event", event}, attrs...)
	logger.Log(context.Background(), level, "audit event", attrs...)
}
