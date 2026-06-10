package agent

import (
	"encoding/json"
	"sort"

	"elbot/internal/llm"
)

type sessionMetadata struct {
	DiscoveredTools []string   `json:"discovered_tools,omitempty"`
	LastUsage       *llm.Usage `json:"last_usage,omitempty"`
}

func decodeSessionMetadata(raw string) sessionMetadata {
	if raw == "" {
		return sessionMetadata{}
	}
	var metadata sessionMetadata
	_ = json.Unmarshal([]byte(raw), &metadata)
	metadata.DiscoveredTools = sortedUnique(metadata.DiscoveredTools)
	return metadata
}

func encodeSessionMetadata(metadata sessionMetadata) string {
	metadata.DiscoveredTools = sortedUnique(metadata.DiscoveredTools)
	if metadata.LastUsage != nil && metadata.LastUsage.TotalTokens <= 0 && metadata.LastUsage.CacheHitTokens <= 0 && metadata.LastUsage.PromptTokens <= 0 && metadata.LastUsage.CompletionTokens <= 0 {
		metadata.LastUsage = nil
	}
	data, _ := json.Marshal(metadata)
	if string(data) == "{}" {
		return ""
	}
	return string(data)
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
