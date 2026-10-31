package builtin

import (
	"slices"
	"testing"

	"elbot/internal/llm"
)

func TestSendFileSchemaRequiresSource(t *testing.T) {
	schema := (SendFileTool{}).Schema()
	if !slices.Contains(schema.Parameters["required"].([]string), "source") {
		t.Fatalf("required = %#v", schema.Parameters["required"])
	}
}

func TestReadFileGlobSchema(t *testing.T) {
	properties := (ReadFileTool{}).Schema().Parameters["properties"].(map[string]any)
	glob := properties["glob"].(map[string]any)
	if glob["type"] != "array" || glob["items"].(map[string]any)["type"] != "string" {
		t.Fatalf("glob = %#v", glob)
	}
}

func TestMediaSelectionSchema(t *testing.T) {
	for _, schema := range []llm.ToolSchema{(GetMediaTool{}).Schema(), (ViewImageTool{}).Schema()} {
		properties := schema.Parameters["properties"].(map[string]any)
		messages := properties["message_id"].(map[string]any)
		outer := properties["media_index"].(map[string]any)
		inner := outer["items"].(map[string]any)
		item := inner["items"].(map[string]any)
		if messages["minItems"] != 1 || outer["minItems"] != 1 || inner["minItems"] != 1 || item["minimum"] != 1 {
			t.Fatalf("%s selection constraints = %#v", schema.Name, properties)
		}
	}
}

func TestToolOperationEnums(t *testing.T) {
	for _, tc := range []struct {
		schema llm.ToolSchema
		field  string
		want   []string
	}{
		{(CronWriteTool{}).Schema(), "operation", []string{"create", "update", "disable", "delete"}},
		{(CronWriteTool{}).Schema(), "schedule_mode", []string{"once", "cron"}},
		{(CronWriteTool{}).Schema(), "trigger_mode", []string{"direct", "llm"}},
		{(ResidentMemoryReadTool{}).Schema(), "section", []string{"all", "core", "normal"}},
		{(LongMemoryWriteTool{}).Schema(), "operation", []string{"save", "update", "delete"}},
	} {
		properties := tc.schema.Parameters["properties"].(map[string]any)
		if got := properties[tc.field].(map[string]any)["enum"].([]string); !slices.Equal(got, tc.want) {
			t.Fatalf("%s.%s = %v, want %v", tc.schema.Name, tc.field, got, tc.want)
		}
	}
}
