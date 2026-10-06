package llm

import (
	"fmt"
	"sort"
)

type ExtraFields struct {
	Source string
	Fields map[string]any
}

// AddExtraFields returns a new body. Extras only add top-level fields; duplicate
// keys (including equal values) and protocol-owned names are rejected.
func AddExtraFields(body map[string]any, owned []string, extras ...ExtraFields) (map[string]any, error) {
	result := make(map[string]any, len(body))
	sources := make(map[string]string, len(body)+len(owned))
	for k, v := range body {
		result[k] = v
		sources[k] = "request"
	}
	for _, k := range owned {
		sources[k] = "protocol request"
	}
	for _, extra := range extras {
		keys := make([]string, 0, len(extra.Fields))
		for k := range extra.Fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if source, exists := sources[k]; exists {
				return nil, fmt.Errorf("extra field %q from %s conflicts with %s; extra parameters cannot override existing fields", k, extra.Source, source)
			}
			result[k] = extra.Fields[k]
			sources[k] = extra.Source
		}
	}
	return result, nil
}
