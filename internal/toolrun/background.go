package toolrun

import (
	"context"

	"elbot/internal/tool"
)

func backgroundToolAllowed(ctx context.Context, item CachedTool, registry *tool.Registry) bool {
	for _, name := range []string{item.Name, item.CanonicalName, item.Schema.Name} {
		if name == "discover_tool" || name == "workspace" {
			return false
		}
	}
	return !item.ForegroundOnly && cachedToolAvailable(ctx, item, registry)
}

// BackgroundCachedTools filters declarations before their initial commit.
func BackgroundCachedTools(ctx context.Context, items []CachedTool, registry *tool.Registry) []CachedTool {
	var result []CachedTool
	for _, item := range items {
		if backgroundToolAllowed(ctx, item, registry) {
			result = append(result, item)
		}
	}
	return result
}

func BackgroundToolNames(ctx context.Context, items []CachedTool, registry *tool.Registry) map[string]bool {
	names := map[string]bool{}
	for _, item := range BackgroundCachedTools(ctx, items, registry) {
		if name := item.Schema.Name; name != "" {
			names[name] = true
		}
	}
	return names
}
