package app

import (
	"context"
	"log/slog"
	"time"
)

// writeLog checks Handler errors; slog.Logger.Log intentionally discards them.
// Callers return failures to the executor's separate diagnostic logger.
func writeLog(ctx context.Context, logger *slog.Logger, at time.Time, level slog.Level, message string, attrs ...any) error {
	if logger == nil || !logger.Enabled(ctx, level) {
		return nil
	}
	if at.IsZero() {
		at = time.Now()
	}
	record := slog.NewRecord(at, level, message, 0)
	record.Add(attrs...)
	return logger.Handler().Handle(ctx, record)
}
