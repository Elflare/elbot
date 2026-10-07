package toolrun

import (
	"context"

	"elbot/internal/tool"
)

func AvailableInContext(ctx context.Context, info tool.Info) bool {
	return tool.InfoAvailableInContext(ctx, info)
}

func unavailableReason(ctx context.Context, info tool.Info) string {
	if reason := tool.UnavailableReason(ctx, info); reason != "" {
		return reason
	}
	return "tool is unavailable in this context"
}
