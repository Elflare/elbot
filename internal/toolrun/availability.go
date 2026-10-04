package toolrun

import (
	"context"

	sandboxctx "elbot/internal/sandbox"
	"elbot/internal/tool"
)

func AvailableInContext(ctx context.Context, info tool.Info) bool {
	return tool.InfoAvailableInContext(ctx, info)
}

func unavailableReason(ctx context.Context, info tool.Info) string {
	if info.ForegroundOnly && sandboxctx.BackgroundContext(ctx) {
		return "tool is only available in foreground sessions"
	}
	return "tool is unavailable in this context"
}
