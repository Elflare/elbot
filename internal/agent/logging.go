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
	if a.execution != nil {
		a.execution.logger = logger
	}
	if a.chat != nil {
		a.chat.logger = logger
	}
	if a.hooks != nil {
		a.hooks.logger = logger
	}
	if a.output != nil {
		a.output.logger = logger
	}
}

func (a *Agent) SetLogManager(logs LogManager) {
	if logs == nil {
		a.SetLogger(nil)
		a.auditLogger = nil
		return
	}
	a.SetLogger(logs.Runtime())
	a.auditLogger = logs.Audit()
}

func (a *Agent) audit(event string, attrs ...any) {
	writeAudit(a.auditLogger, slog.LevelInfo, event, attrs...)
}

func writeAudit(logger *slog.Logger, level slog.Level, event string, attrs ...any) {
	if logger == nil {
		return
	}
	attrs = append([]any{"event", event}, attrs...)
	logger.Log(context.Background(), level, "audit event", attrs...)
}
