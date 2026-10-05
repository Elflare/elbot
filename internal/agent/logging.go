package agent

import (
	"context"
	"log/slog"
)

type LogManager interface {
	Runtime() *slog.Logger
	Audit() *slog.Logger
}

func writeAudit(logger *slog.Logger, level slog.Level, event string, attrs ...any) {
	if logger == nil {
		return
	}
	attrs = append([]any{"event", event}, attrs...)
	logger.Log(context.Background(), level, "audit event", attrs...)
}
