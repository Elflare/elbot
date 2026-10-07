package signal

import (
	"context"
	"log/slog"
	"os"
)

// Infrastructure failures must remain observable even when business logging is
// unavailable. Never send these through a signal or a configurable file logger.
func reportFailure(message string, attrs ...slog.Attr) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	logger.LogAttrs(context.Background(), slog.LevelError, message, attrs...)
}
