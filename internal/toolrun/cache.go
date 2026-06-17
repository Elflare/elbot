package toolrun

import "encoding/json"

type Cache struct {
	Tools []CachedTool `json:"tools,omitempty"`
}

func DecodeCache(raw json.RawMessage) Cache {
	if len(raw) == 0 {
		return Cache{}
	}
	var cache Cache
	_ = json.Unmarshal(raw, &cache)
	cache.Tools = NormalizeCachedTools(cache.Tools)
	return cache
}

func EncodeCache(cache Cache) json.RawMessage {
	cache.Tools = NormalizeCachedTools(cache.Tools)
	if len(cache.Tools) == 0 {
		return nil
	}
	data, _ := json.Marshal(cache)
	return data
}

func NormalizeCachedTools(tools []CachedTool) []CachedTool {
	seen := map[string]bool{}
	out := make([]CachedTool, 0, len(tools))
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
			cached.Source = SourceKindNative
		}
		out = append(out, cached)
	}
	return SortCachedTools(out)
}

func MergeCachedTools(existing, incoming []CachedTool) []CachedTool {
	byKey := map[string]CachedTool{}
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
	out := make([]CachedTool, 0, len(byKey))
	for _, cached := range byKey {
		out = append(out, cached)
	}
	return NormalizeCachedTools(out)
}
