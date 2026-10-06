package llm

import (
	"reflect"
	"strings"
	"testing"
)

func TestAddExtraFieldsAddsWithoutMutatingInputs(t *testing.T) {
	body := map[string]any{"model": "m"}
	provider := map[string]any{"reasoning": map[string]any{"effort": "medium"}}
	model := map[string]any{"text": map[string]any{"format": map[string]any{"type": "json_object"}}}
	result, err := AddExtraFields(body, []string{"tools"}, ExtraFields{"provider", provider}, ExtraFields{"model", model}, ExtraFields{"request", map[string]any{"seed": 42}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 4 || result["model"] != "m" || !reflect.DeepEqual(result["reasoning"], provider["reasoning"]) || !reflect.DeepEqual(result["text"], model["text"]) {
		t.Fatalf("body=%#v", result)
	}
	if len(body) != 1 || len(provider) != 1 || len(model) != 1 {
		t.Fatal("input maps were modified")
	}
}

func TestAddExtraFieldsRejectsAllConflicts(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		body      map[string]any
		owned     []string
		extras    []ExtraFields
		source    string
	}{
		{"request field", "model", map[string]any{"model": "m"}, nil, []ExtraFields{{"provider", map[string]any{"model": "other"}}}, "request"},
		{"omitted owned field", "tools", map[string]any{}, []string{"tools"}, []ExtraFields{{"provider", map[string]any{"tools": []any{}}}}, "protocol request"},
		{"equal values", "seed", map[string]any{}, nil, []ExtraFields{{"provider", map[string]any{"seed": 42}}, {"model", map[string]any{"seed": 42}}}, "provider"},
		{"nested object", "reasoning", map[string]any{}, nil, []ExtraFields{{"provider", map[string]any{"reasoning": map[string]any{"effort": "medium"}}}, {"request", map[string]any{"reasoning": map[string]any{"summary": "auto"}}}}, "provider"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := AddExtraFields(tc.body, tc.owned, tc.extras...)
			if err == nil || !strings.Contains(err.Error(), tc.key) || !strings.Contains(err.Error(), tc.source) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
