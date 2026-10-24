// 整理后台预加载所需的工具声明和工具名单。
package preload

import toolshare "elbot/internal/tool/share"

import "context"

func backgroundToolAllowed(ctx context.Context, item toolshare.DiscoveredTool) bool {
	for _, name := range []string{item.Name, item.CanonicalName, item.Schema.Name} {
		if name == "discover_tool" || name == "workspace" {
			return false
		}
	}
	return !item.ForegroundOnly && cachedToolAvailable(ctx, item)
}

// BackgroundCachedTools filters declarations before their initial commit.
func BackgroundCachedTools(ctx context.Context, items []toolshare.DiscoveredTool) []toolshare.DiscoveredTool {
	var result []toolshare.DiscoveredTool
	for _, item := range items {
		if backgroundToolAllowed(ctx, item) {
			result = append(result, item)
		}
	}
	return result
}

func BackgroundToolNames(ctx context.Context, items []toolshare.DiscoveredTool) map[string]bool {
	names := map[string]bool{}
	for _, item := range BackgroundCachedTools(ctx, items) {
		if name := item.Schema.Name; name != "" {
			names[name] = true
		}
	}
	return names
}
