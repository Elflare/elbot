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
}

func (a *Agent) SetLogManager(logs LogManager) {
	if logs == nil {
		a.logger = nil
		a.auditLogger = nil
		return
	}
	a.logger = logs.Runtime()
	a.auditLogger = logs.Audit()
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
	if a.auditLogger == nil {
		return
	}
	attrs = append([]any{"event", event}, attrs...)
	a.auditLogger.Log(context.Background(), level, "audit event", attrs...)
}
