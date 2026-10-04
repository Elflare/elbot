package tool

import (
	"context"

	sandboxctx "elbot/internal/sandbox"
)

func InfoAvailableInContext(ctx context.Context, info Info) bool {
	return !info.ForegroundOnly || !sandboxctx.BackgroundContext(ctx)
}
