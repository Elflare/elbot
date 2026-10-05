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
	if a.caller != nil {
		a.caller.logger = logger
	}
	if a.toolDeps != nil {
		a.toolDeps.logger = logger
	}
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
		if a.execution != nil {
			a.execution.auditLogger = nil
		}
		if a.chat != nil {
			a.chat.auditLogger = nil
		}
		if a.caller != nil {
			a.caller.auditLogger = nil
		}
		if a.confirmations != nil {
			a.confirmations.auditLogger = nil
		}
		if a.toolDeps != nil {
			a.toolDeps.auditLogger = nil
		}
		if a.replies != nil {
			a.replies.auditLogger = nil
		}
		return
	}
	a.SetLogger(logs.Runtime())
	a.auditLogger = logs.Audit()
	if a.execution != nil {
		a.execution.auditLogger = a.auditLogger
	}
	if a.chat != nil {
		a.chat.auditLogger = a.auditLogger
	}
	if a.caller != nil {
		a.caller.auditLogger = a.auditLogger
	}
	if a.confirmations != nil {
		a.confirmations.auditLogger = a.auditLogger
	}
	if a.toolDeps != nil {
		a.toolDeps.auditLogger = a.auditLogger
	}
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
