package agent

import (
	"encoding/json"
	"sort"

	"elbot/internal/llm"
	"elbot/internal/toolrun"
)

type sessionMetadata struct {
	DiscoveredTools []string             `json:"discovered_tools,omitempty"`
	ToolCache       []toolrun.CachedTool `json:"tool_cache,omitempty"`
	ToolTags        []string             `json:"tool_tags,omitempty"`
	LastUsage       *llm.Usage           `json:"last_usage,omitempty"`
	BackgroundKind  string               `json:"background_kind,omitempty"`
	WorkspaceDir    string               `json:"workspace_dir,omitempty"`
}

func decodeSessionMetadata(raw string) sessionMetadata {
	if raw == "" {
		return sessionMetadata{}
	}
	var metadata sessionMetadata
	_ = json.Unmarshal([]byte(raw), &metadata)
	metadata.DiscoveredTools = sortedUnique(metadata.DiscoveredTools)
	metadata.ToolCache = toolCacheItemsNormalized(metadata.ToolCache)
	metadata.ToolTags = sortedUnique(metadata.ToolTags)
	return metadata
}

func encodeSessionMetadata(metadata sessionMetadata) string {
	return encodeSessionMetadataInto("", metadata)
}

func encodeSessionMetadataInto(raw string, metadata sessionMetadata) string {
	metadata.DiscoveredTools = sortedUnique(metadata.DiscoveredTools)
	metadata.ToolCache = toolCacheItemsNormalized(metadata.ToolCache)
	metadata.ToolTags = sortedUnique(metadata.ToolTags)
	if metadata.LastUsage != nil && metadata.LastUsage.TotalTokens <= 0 && metadata.LastUsage.CacheHitTokens <= 0 && metadata.LastUsage.PromptTokens <= 0 && metadata.LastUsage.CompletionTokens <= 0 {
		metadata.LastUsage = nil
	}
	base := map[string]any{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &base)
	}
	setMetadataField(base, "discovered_tools", metadata.DiscoveredTools)
	setMetadataField(base, "tool_cache", metadata.ToolCache)
	setMetadataField(base, "tool_tags", metadata.ToolTags)
	setMetadataField(base, "last_usage", metadata.LastUsage)
	setMetadataField(base, "background_kind", metadata.BackgroundKind)
	setMetadataField(base, "workspace_dir", metadata.WorkspaceDir)
	data, _ := json.Marshal(base)
	if string(data) == "{}" {
		return ""
	}
	return string(data)
}

func setMetadataField(data map[string]any, key string, value any) {
	switch typed := value.(type) {
	case string:
		if typed == "" {
			delete(data, key)
			return
		}
	case []string:
		if len(typed) == 0 {
			delete(data, key)
			return
		}
	case []toolrun.CachedTool:
		if len(typed) == 0 {
			delete(data, key)
			return
		}
	case *llm.Usage:
		if typed == nil {
			delete(data, key)
			return
		}
	case nil:
		delete(data, key)
		return
	}
	data[key] = value
}

func sortedUnique(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func toolCacheItemsNormalized(items []toolrun.CachedTool) []toolrun.CachedTool {
	return toolrun.NormalizeCachedTools(items)
}
