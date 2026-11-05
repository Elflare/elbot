package share

import (
	"context"

	"elbot/internal/events"
)

// LogBackground publishes a complete record from a background operation source.
// The source owns all nine fields, including identity, level and result. This
// entrypoint does not inspect business returns or collect tool/dialogue records.
func LogBackground(ctx context.Context, record events.LogRecord) {
	events.Log(context.WithoutCancel(ctx), record)
}
