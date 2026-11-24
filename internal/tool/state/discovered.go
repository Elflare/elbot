// 规范化、合并和排序已发现的工具声明。
package state

import (
	"cmp"
	"sort"

	toolshare "elbot/internal/tool/share"
)

func discoveredNames(tools []toolshare.DiscoveredTool) map[string]bool {
	names := map[string]bool{}
	for _, item := range tools {
		name := cmp.Or(item.CanonicalName, item.Name)
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
		key := cmp.Or(discovered.CanonicalName, discovered.Name)
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
	for _, tools := range [][]toolshare.DiscoveredTool{existing, incoming} {
		for _, discovered := range tools {
			key := cmp.Or(discovered.CanonicalName, discovered.Name)
			if key != "" {
				byKey[key] = discovered
			}
		}
	}
	out := make([]toolshare.DiscoveredTool, 0, len(byKey))
	for _, discovered := range byKey {
		out = append(out, discovered)
	}
	return sortDiscoveredTools(out)
}

func sortDiscoveredTools(tools []toolshare.DiscoveredTool) []toolshare.DiscoveredTool {
	if len(tools) == 0 {
		return nil
	}
	sort.Slice(tools, func(i, j int) bool {
		return cmp.Or(tools[i].CanonicalName, tools[i].Name) < cmp.Or(tools[j].CanonicalName, tools[j].Name)
	})
	return tools
}
