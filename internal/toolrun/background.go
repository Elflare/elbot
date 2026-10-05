package toolrun

import "context"

func backgroundToolAllowed(ctx context.Context, item CachedTool) bool {
	for _, name := range []string{item.Name, item.CanonicalName, item.Schema.Function.Name} {
		if name == "discover_tool" || name == "workspace" {
			return false
		}
	}
	return !item.ForegroundOnly && cachedToolAvailable(ctx, item)
}

// BackgroundCachedTools filters declarations before their initial commit.
func BackgroundCachedTools(ctx context.Context, items []CachedTool) []CachedTool {
	var result []CachedTool
	for _, item := range items {
		if backgroundToolAllowed(ctx, item) {
			result = append(result, item)
		}
	}
	return result
}

func BackgroundToolNames(ctx context.Context, items []CachedTool) map[string]bool {
	names := map[string]bool{}
	for _, item := range BackgroundCachedTools(ctx, items) {
		if name := item.Schema.Function.Name; name != "" {
			names[name] = true
		}
	}
	return names
}
