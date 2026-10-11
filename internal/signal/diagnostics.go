package signal

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
)

type diagnostic struct {
	writer  *os.File
	message string
	attrs   []slog.Attr
	barrier chan struct{}
}

var diagnostics = make(chan diagnostic, 64)
var diagnosticWorker sync.Once
var droppedDiagnostics atomic.Uint64

func startDiagnostics() {
	diagnosticWorker.Do(func() {
		go func() {
			var writer *os.File
			var logger *slog.Logger
			for d := range diagnostics {
				if d.barrier != nil {
					close(d.barrier)
					continue
				}
				if writer != d.writer {
					writer = d.writer
					logger = slog.New(slog.NewTextHandler(writer, nil))
				}
				if dropped := droppedDiagnostics.Swap(0); dropped != 0 {
					logger.Warn("signal diagnostics dropped", "count", dropped)
				}
				logger.LogAttrs(context.Background(), slog.LevelError, d.message, d.attrs...)
			}
		}()
	})
}

// Diagnostic I/O has one independent bounded worker. A blocked stderr never
// holds an execution slot; overflow is counted and coalesced on recovery.
func reportFailure(message string, attrs ...slog.Attr) {
	startDiagnostics()
	select {
	case diagnostics <- diagnostic{writer: os.Stderr, message: message, attrs: attrs}:
	default:
		droppedDiagnostics.Add(1)
	}
}

// FlushDiagnostics waits for previously admitted diagnostics. Callers must bound
// ctx because stderr may be blocked. Normal signal execution never calls this.
func FlushDiagnostics(ctx context.Context) error {
	startDiagnostics()
	done := make(chan struct{})
	select {
	case diagnostics <- diagnostic{barrier: done}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
