// 规范化、去重和合并工具缓存条目。
package state

import toolshare "elbot/internal/tool/share"

func NormalizeCachedTools(tools []toolshare.DiscoveredTool) []toolshare.DiscoveredTool {
	seen := map[string]bool{}
	out := make([]toolshare.DiscoveredTool, 0, len(tools))
	for _, cached := range tools {
		key := cached.CanonicalName
		if key == "" {
			key = cached.Name
		}
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		if cached.Source == "" {
			cached.Source = toolshare.SourceKindNative
		}
		out = append(out, cached)
	}
	return SortCachedTools(out)
}

func MergeCachedTools(existing, incoming []toolshare.DiscoveredTool) []toolshare.DiscoveredTool {
	byKey := map[string]toolshare.DiscoveredTool{}
	for _, cached := range existing {
		key := cached.CanonicalName
		if key == "" {
			key = cached.Name
		}
		if key != "" {
			byKey[key] = cached
		}
	}
	for _, cached := range incoming {
		key := cached.CanonicalName
		if key == "" {
			key = cached.Name
		}
		if key != "" {
			byKey[key] = cached
		}
	}
	out := make([]toolshare.DiscoveredTool, 0, len(byKey))
	for _, cached := range byKey {
		out = append(out, cached)
	}
	return NormalizeCachedTools(out)
}
