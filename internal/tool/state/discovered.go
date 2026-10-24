// 规范化、合并和排序已发现的工具声明。
package state

import (
	"sort"

	toolshare "elbot/internal/tool/share"
)

func discoveredNames(tools []toolshare.DiscoveredTool) map[string]bool {
	names := map[string]bool{}
	for _, item := range tools {
		name := item.CanonicalName
		if name == "" {
			name = item.Name
		}
		if name != "" {
			names[name] = true
		}
	}
	return names
}

func normalizeDiscoveredTools(tools []toolshare.DiscoveredTool) []toolshare.DiscoveredTool {
	seen := map[string]bool{}
	out := make([]toolshare.DiscoveredTool, 0, len(tools))
	for _, discovered := range tools {
		key := discovered.CanonicalName
		if key == "" {
			key = discovered.Name
		}
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, discovered)
	}
	return sortDiscoveredTools(out)
}

func mergeDiscoveredTools(existing, incoming []toolshare.DiscoveredTool) []toolshare.DiscoveredTool {
	byKey := map[string]toolshare.DiscoveredTool{}
	for _, discovered := range existing {
		key := discovered.CanonicalName
		if key == "" {
			key = discovered.Name
		}
		if key != "" {
			byKey[key] = discovered
		}
	}
	for _, discovered := range incoming {
		key := discovered.CanonicalName
		if key == "" {
			key = discovered.Name
		}
		if key != "" {
			byKey[key] = discovered
		}
	}
	out := make([]toolshare.DiscoveredTool, 0, len(byKey))
	for _, discovered := range byKey {
		out = append(out, discovered)
	}
	return sortDiscoveredTools(out)
}

func sortDiscoveredTools(tools []toolshare.DiscoveredTool) []toolshare.DiscoveredTool {
	out := append([]toolshare.DiscoveredTool(nil), tools...)
	sort.Slice(out, func(i, j int) bool {
		left, right := out[i].CanonicalName, out[j].CanonicalName
		if left == "" {
			left = out[i].Name
		}
		if right == "" {
			right = out[j].Name
		}
		return left < right
	})
	return out
}
