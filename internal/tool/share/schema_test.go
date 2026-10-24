package share

import (
	"reflect"
	"testing"

	"elbot/internal/llm"
)

func TestCloneSchemaDetachesJSONContainers(t *testing.T) {
	original := llm.ToolSchema{Name: "example", Parameters: map[string]any{
		"properties": map[string]any{"count": map[string]any{"minimum": int64(1)}},
		"required":   []string{"count"},
		"enum":       []any{map[string]any{"value": "original"}},
		"nil_map":    map[string]any(nil),
		"nil_slice":  []string(nil),
		"nil":        nil,
	}}
	cloned := CloneSchema(original)
	if !reflect.DeepEqual(cloned, original) {
		t.Fatal("clone changed schema values or scalar types")
	}
	cloned.Parameters["properties"].(map[string]any)["count"].(map[string]any)["minimum"] = int64(2)
	cloned.Parameters["required"].([]string)[0] = "changed"
	cloned.Parameters["enum"].([]any)[0].(map[string]any)["value"] = "changed"
	if original.Parameters["properties"].(map[string]any)["count"].(map[string]any)["minimum"] != int64(1) ||
		original.Parameters["required"].([]string)[0] != "count" ||
		original.Parameters["enum"].([]any)[0].(map[string]any)["value"] != "original" {
		t.Fatal("mutating clone changed original schema")
	}
	if CloneSchema(llm.ToolSchema{}).Parameters != nil {
		t.Fatal("nil parameters should remain nil")
	}
}
