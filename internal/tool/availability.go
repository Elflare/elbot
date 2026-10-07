package tool

import (
	"context"
	"slices"

	"elbot/internal/contextinfo"
	"elbot/internal/llm"
	sandboxctx "elbot/internal/sandbox"
)

func InfoAvailableInContext(ctx context.Context, info Info) bool {
	return UnavailableReason(ctx, info) == ""
}

func UnavailableReason(ctx context.Context, info Info) string {
	if info.ForegroundOnly && sandboxctx.BackgroundContext(ctx) {
		return "tool is only available in foreground sessions"
	}
	if len(info.APITypes) > 0 {
		model, ok := contextinfo.ModelFromContext(ctx)
		if !ok || model.APIType == "" || !slices.Contains(info.APITypes, llm.APIType(model.APIType)) {
			return "tool is unavailable for the current API type"
		}
	}
	return ""
}
