package llm

import (
	"fmt"
	"maps"
)

// RequestConfig applies the native choice and prepares request-local configuration.
// Only provider/model defaults are omitted; request ExtraBody is merged strictly.
func RequestConfig(body map[string]any, choice string, provider, model map[string]any, omit ...string) (map[string]any, map[string]any) {
	if choice != "" {
		body["tool_choice"] = choice
	}
	if choice == "none" {
		omit = append(omit, "tool_choice")
	}
	if len(omit) != 0 {
		provider, model = maps.Clone(provider), maps.Clone(model)
		for _, key := range omit {
			delete(provider, key)
			delete(model, key)
		}
	}
	return provider, model
}

// AddExtraFields rejects duplicate keys and overrides of protocol-owned fields.
func AddExtraFields(body map[string]any, owned []string, provider, model, request map[string]any) (map[string]any, error) {
	result := make(map[string]any, len(body))
	sources := make(map[string]string, len(body)+len(owned))
	for k, v := range body {
		result[k] = v
		sources[k] = "request"
	}
	for _, k := range owned {
		sources[k] = "protocol request"
	}
	labels := [...]string{"provider extra_payload", "model extra_payload", "request ExtraBody"}
	for i, fields := range []map[string]any{provider, model, request} {
		for k, v := range fields {
			if source, exists := sources[k]; exists {
				return nil, fmt.Errorf("extra field %q from %s conflicts with %s; extra parameters cannot override existing fields", k, labels[i], source)
			}
			result[k] = v
			sources[k] = labels[i]
		}
	}
	return result, nil
}
